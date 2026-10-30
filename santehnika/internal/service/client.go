package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/fhttp/cookiejar"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/meoyawn/clis/santehnika/internal/domain"
)

type BlockedError struct {
	URL    string
	Status int
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("catalog protection rejected the request (HTTP %d); complete the check in Chrome, then run auth import and retry manually", e.Status)
}

type Client struct {
	http     tlsclient.HttpClient
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
	if err := s.checkKazan(); err != nil {
		return nil, err
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
	c := &Client{origin: Origin, session: s}
	if s.PageURL != "" {
		if _, err := c.pageURL(s.PageURL); err != nil {
			return nil, fmt.Errorf("invalid saved page URL: %w", err)
		}
	}
	// Reusing Chrome cookies also requires a compatible TLS/HTTP2 handshake.
	// A User-Agent override alone still receives Servicepipe challenge HTML.
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(profiles.Chrome_152),
		tlsclient.WithCookieJar(jar),
		tlsclient.WithTimeoutSeconds(60),
		tlsclient.WithDisableHttp3(),
		tlsclient.WithCustomRedirectFunc(func(req *http.Request, via []*http.Request) error {
			if len(via) > 4 || req.URL.Scheme != "https" || req.URL.Hostname() != origin.Hostname() {
				return fmt.Errorf("unexpected catalog redirect")
			}
			c.requests++
			return nil
		}),
	}
	proxy, err := stdhttp.ProxyFromEnvironment(&stdhttp.Request{URL: origin})
	if err != nil {
		return nil, fmt.Errorf("catalog proxy configuration: %w", err)
	}
	if proxy != nil {
		options = append(options, tlsclient.WithProxyUrl(proxy.String()))
	}
	c.http, err = tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, fmt.Errorf("create browser-compatible HTTP client: %w", err)
	}
	return c, nil
}
func (c *Client) fetch(ctx context.Context, method, target, contentType string, body []byte, extra http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.session.UserAgent)
	req.Header.Set("Accept-Language", "en,tt;q=0.9,ru;q=0.8")
	referer := c.session.PageURL
	if referer == "" {
		referer = c.origin + "/"
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("DNT", "1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if _, version, ok := strings.Cut(c.session.UserAgent, "Chrome/"); ok {
		major, _, _ := strings.Cut(version, ".")
		req.Header.Set("Sec-Ch-Ua", fmt.Sprintf(`"Google Chrome";v=%q, "Chromium";v=%q, "Not(A:Brand";v="24"`, major, major))
		req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
		req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	}
	if method == http.MethodPost {
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Priority", "u=1, i")
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/jxl,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
		req.Header.Set("Priority", "u=0, i")
	}
	req.Header[http.HeaderOrderKey] = []string{
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "upgrade-insecure-requests",
		"user-agent", "accept", "content-type", "origin", "sec-fetch-site", "sec-fetch-mode",
		"sec-fetch-user", "sec-fetch-dest", "referer", "accept-encoding", "accept-language", "cookie", "priority",
	}
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
	if err := c.session.checkKazan(); err != nil {
		return nil, fmt.Errorf("catalog changed region: %w", err)
	}
	return b, nil
}

func (s Session) checkKazan() error {
	for _, name := range []string{"_lcid", "_laddr", "_lcname", "_llocality", "_lregname"} {
		found := false
		for _, cookie := range s.Cookies {
			if cookie.Name != name || !validCookie(cookie) || (cookie.Expires != nil && !cookie.Expires.After(time.Now())) {
				continue
			}
			if name == "_lcid" && cookie.Value != "1283" {
				return fmt.Errorf("city ID %q is not Kazan (1283); select Kazan in Chrome, then import that profile again", cookie.Value)
			}
			found = true
			break
		}
		if !found {
			return fmt.Errorf("missing Kazan location cookie %q; select Kazan in Chrome, then import that profile again", name)
		}
	}
	return nil
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
	return &domain.Result{URL: target, Products: []domain.Product{}, HTTPRequests: c.requests}
}
func trimResult(r *domain.Result, limit int) {
	r.PageSize = len(r.Products)
	if limit > 0 && len(r.Products) > limit {
		r.Products = r.Products[:limit]
	}
}
