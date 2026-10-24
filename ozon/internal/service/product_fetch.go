package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const maxProductRedirects = 3

// Product redirects may canonicalize the slug or update navigation state, but
// must retain the SKU, section, pagination, review scope and seller sort.
func productPageURL(raw, sku string) (*url.URL, error) {
	clean, err := safePageURL(raw)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(clean)
	if err != nil {
		return nil, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid product page parameters")
	}
	if u.Path == "/modal/otherOffersFromSellers" {
		if q.Get("product_id") != sku {
			return nil, fmt.Errorf("seller overlay does not belong to SKU %s", sku)
		}
	} else if m := productPattern.FindStringSubmatch(u.Path); m == nil || m[1] != sku {
		return nil, fmt.Errorf("product page does not belong to SKU %s", sku)
	}
	for _, values := range q {
		if len(values) != 1 || values[0] == "" {
			return nil, fmt.Errorf("product page parameters must have one nonempty value")
		}
	}
	// Ozon can echo API navigation state into the nested page URL.
	q.Del("__rr")
	u.RawQuery = q.Encode()
	return u, nil
}

func sameProductRedirect(want, got *url.URL) bool {
	section := func(path string) string {
		if path == "/modal/otherOffersFromSellers" {
			return path
		}
		return strings.Join(strings.Split(strings.Trim(path, "/"), "/")[2:], "/")
	}
	if section(want.Path) != section(got.Path) {
		return false
	}
	wantQuery, gotQuery := want.Query(), got.Query()
	for _, pair := range [][2]url.Values{{wantQuery, gotQuery}, {gotQuery, wantQuery}} {
		for key, values := range pair[0] {
			if key != "__rr" && key != "page_changed" && !slices.Equal(values, pair[1][key]) {
				return false
			}
		}
	}
	return true
}

func (c *Client) fetchProduct(ctx context.Context, path, sku string, opt RequestOptions) (*page, error) {
	current, err := productPageURL(path, sku)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target := entrypointURL(current.String())
	cookies, err := c.auth.cookiesFor(target)
	if err != nil {
		return nil, err
	}
	// Navigation cookies remain local to this fetch and cannot replace saved
	// account credentials or leak into another invocation.
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	jar.SetCookies(target, cookies)
	cookiesFor := func(u *url.URL) ([]*http.Cookie, error) { return jar.Cookies(u), nil }
	visited := map[string]bool{target.String(): true}
	var requests int
	var sources []string
	for redirects := 0; ; redirects++ {
		p, err := c.fetchPage(ctx, current.String(), target, opt, cookiesFor)
		if p != nil {
			requests += p.meta.HTTPRequests
			sources = append(sources, p.meta.SourceURLs...)
			p.meta.HTTPRequests, p.meta.SourceURLs = requests, sources
		}
		var redirect *pageRedirectError
		if !errors.As(err, &redirect) {
			return p, err
		}
		if redirects == maxProductRedirects {
			return p, fmt.Errorf("%w: exceeded %d product redirects", err, maxProductRedirects)
		}
		next, nextTarget, validationErr := pageRedirectURL(redirect.target, func(raw string) (*url.URL, error) {
			return productPageURL(raw, sku)
		})
		if validationErr != nil || !sameProductRedirect(current, next) {
			// Redirect targets may contain private session state.
			return p, fmt.Errorf("%w: invalid product redirect", err)
		}
		if visited[nextTarget.String()] {
			return p, fmt.Errorf("%w: product redirect loop", err)
		}
		visited[nextTarget.String()] = true
		jar.SetCookies(target, redirect.cookies)
		current, target = next, nextTarget
	}
}
