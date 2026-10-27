package service

import (
	"bytes"
	"context"
	"fmt"
	"github.com/meoyawn/clis/lemana/internal/domain"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type BlockedError struct {
	URL    string
	Status int
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("catalog protection rejected the request (HTTP %d); complete the check in Chrome, then run auth import and retry manually", e.Status)
}

type Client struct {
	http     *http.Client
	origin   string
	session  Session
	requests int
}

func NewClient(s Session) (*Client, error) {
	if strings.ContainsAny(s.UserAgent, "\r\n") {
		return nil, fmt.Errorf("user-agent must not contain newlines")
	}
	if s.UserAgent == "" {
		s.UserAgent = BrowserUserAgent
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	origin, _ := url.Parse(Origin)
	for _, c := range s.Cookies {
		if validCookie(c) && (c.Expires == nil || c.Expires.After(time.Now())) {
			h := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
			if c.Expires != nil {
				h.Expires = *c.Expires
			}
			jar.SetCookies(origin, []*http.Cookie{h})
		}
	}
	// The Kazan storefront is fixed even if Chrome last selected another city.
	jar.SetCookies(origin, []*http.Cookie{{Name: "_regionID", Value: "1448", Domain: "lemanapro.ru", Path: "/", Secure: true}, {Name: "_userRegion", Value: "kazan", Domain: "lemanapro.ru", Path: "/", Secure: true}})

	c := &Client{origin: Origin, session: s}
	c.http = &http.Client{Jar: jar, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 4 || req.URL.Scheme != "https" || req.URL.Hostname() != origin.Hostname() {
			return fmt.Errorf("unexpected catalog redirect")
		}
		c.requests++
		return nil
	}}
	return c, nil
}
func (c *Client) fetch(ctx context.Context, method, target, contentType string, body []byte, extra http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.session.UserAgent)
	req.Header.Set("Accept", "application/json, text/html, */*")
	req.Header.Set("Referer", c.origin+"/")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range extra {
		req.Header[k] = vs
	}
	c.requests++
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog request: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 16<<20 {
		return nil, fmt.Errorf("catalog response exceeded 16 MiB")
	}
	lower := strings.ToLower(string(b))
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429 || strings.Contains(lower, "/__qrator/") || strings.Contains(lower, "servicepipe.tech/") || strings.Contains(lower, "ddos-guard") || strings.Contains(lower, "js-challenge-loader") {
		return nil, &BlockedError{URL: target, Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("catalog returned HTTP %d", resp.StatusCode)
	}
	c.mergeCookies(resp.Cookies(), resp.Request.URL.Hostname())
	return b, nil
}
func (c *Client) mergeCookies(incoming []*http.Cookie, host string) {
	for _, h := range incoming {
		v := SavedCookie{Name: h.Name, Value: h.Value, Domain: h.Domain, Path: h.Path, Secure: h.Secure, HTTPOnly: h.HttpOnly}
		if v.Domain == "" {
			v.Domain = host
		}
		if v.Path == "" {
			v.Path = "/"
		}
		if h.MaxAge > 0 {
			expiry := time.Now().Add(time.Duration(h.MaxAge) * time.Second)
			v.Expires = &expiry
		} else if !h.Expires.IsZero() {
			v.Expires = &h.Expires
		}
		check := v
		if check.Value == "" {
			check.Value = "deleted"
		}
		if !validCookie(check) {
			continue
		}
		kept := c.session.Cookies[:0]
		for _, old := range c.session.Cookies {
			if old.Name != v.Name || strings.TrimPrefix(old.Domain, ".") != strings.TrimPrefix(v.Domain, ".") || old.Path != v.Path {
				kept = append(kept, old)
			}
		}
		c.session.Cookies = kept
		if h.MaxAge >= 0 && validCookie(v) && (v.Expires == nil || v.Expires.After(time.Now())) {
			c.session.Cookies = append(c.session.Cookies, v)
		}
	}
}
func (c *Client) Session() Session { return c.session }
func (c *Client) pageURL(input string) (*url.URL, error) {
	base, _ := url.Parse(c.origin)
	u, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("invalid catalog URL")
	}
	u = base.ResolveReference(u)
	if u.Host != base.Host || u.Scheme != base.Scheme || u.User != nil || u.Opaque != "" {
		return nil, fmt.Errorf("URL must belong to %s", base.Host)
	}
	u.Fragment = ""
	return u, nil
}

type Options struct {
	Page           int
	Limit          int
	IncludeFilters bool
	Sort           domain.Sort
	Filters        url.Values
}

func (o Options) Validate() error {
	if o.Page < 1 || o.Limit < 0 {
		return fmt.Errorf("page must be positive and limit must not be negative")
	}
	if _, err := domain.NewSortFromValue(o.Sort.String()); err != nil {
		return err
	}
	return nil
}
func (c *Client) result(target string) *domain.Result {
	return &domain.Result{URL: target, City: "Казань", Products: []domain.Product{}, Warnings: []string{}, HTTPRequests: c.requests}
}
func trimResult(r *domain.Result, limit int) {
	r.PageSize = len(r.Products)
	if limit > 0 && len(r.Products) > limit {
		r.Products = r.Products[:limit]
	}
}
