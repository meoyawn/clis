package service

import (
	"bytes"
	"context"
	"fmt"
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
	if e.Status == 0 {
		return "Chrome did not renew the catalog protection cookie; complete the check in Chrome, then rerun the command"
	}
	return fmt.Sprintf("catalog protection rejected the request (HTTP %d); complete the check in Chrome, then rerun the command", e.Status)
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
	// The jar also keeps PHP session and CSRF cookies for the duration of one
	// command; only allow-listed cookies are persisted afterwards.
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
	c.http = &http.Client{Jar: jar, Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 4 || req.URL.Scheme != "https" || req.URL.Hostname() != origin.Hostname() {
			return fmt.Errorf("unexpected catalog redirect")
		}
		c.requests++
		return nil
	}}
	return c, nil
}

// request describes one catalog call. XHR adds the headers the storefront
// requires for its JSON endpoints.
type request struct {
	method  string
	target  string
	form    url.Values
	xhr     bool
	csrf    string
	referer string
}

type response struct {
	body []byte
	url  *url.URL
}

func (c *Client) fetch(ctx context.Context, r request) (*response, error) {
	var body io.Reader
	if r.form != nil {
		body = strings.NewReader(r.form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, r.method, r.target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.session.UserAgent)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	if r.xhr {
		req.Header.Set("Accept", "*/*")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("Origin", c.origin)
	} else {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	}
	if r.referer != "" {
		req.Header.Set("Referer", r.referer)
	}
	if r.csrf != "" {
		req.Header.Set("X-CSRF-Token", r.csrf)
	}
	if r.form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	if blocked(resp.StatusCode, b) {
		return nil, &BlockedError{URL: r.target, Status: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("catalog returned HTTP %d for %s", resp.StatusCode, r.target)
	}
	c.mergeCookies(resp.Cookies(), resp.Request.URL.Hostname())
	return &response{body: b, url: resp.Request.URL}, nil
}

func blocked(status int, body []byte) bool {
	if status == 401 || status == 403 || status == 429 {
		return true
	}
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("/__qrator/"))
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
	if u.Host == "dns-shop.ru" {
		u.Host = base.Host
	}
	if u.Host != base.Host || u.Scheme != base.Scheme || u.User != nil || u.Opaque != "" {
		return nil, fmt.Errorf("URL must belong to %s", base.Host)
	}
	u.Fragment = ""
	return u, nil
}
