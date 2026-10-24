package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
	"golang.org/x/net/publicsuffix"
)

func BrowserClient(ctx context.Context, base *url.URL, profile, cookieFile string) (*http.Client, error) {
	if base.Scheme != "https" || !IsHHHost(base.Hostname()) || base.User != nil || base.Port() != "" {
		return nil, fmt.Errorf("invalid HH base URL %q: expected https://hh.ru or an hh.ru subdomain", base.Redacted())
	}
	if profile == "" {
		return nil, fmt.Errorf("invalid Chrome profile %q: choose an explicit profile", profile)
	}
	opts := sweetcookie.Options{URL: base.String() + "/", Browsers: []sweetcookie.Browser{sweetcookie.BrowserChrome}, Profiles: map[sweetcookie.Browser]string{sweetcookie.BrowserChrome: profile}, Timeout: 15 * time.Second}
	if cookieFile != "" {
		opts.Browsers = []sweetcookie.Browser{sweetcookie.BrowserInline}
		opts.Inline = sweetcookie.InlineCookies{File: cookieFile}
	}
	result, err := sweetcookie.Get(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("load Chrome cookies via sweetcookie: %w", err)
	}
	if len(result.Cookies) == 0 {
		return nil, fmt.Errorf("no hh.ru cookies in Chrome profile %q; sign in to hh.ru in that profile; sweetcookie warnings: %v", profile, result.Warnings)
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}
	for _, cookie := range result.Cookies {
		host := strings.TrimPrefix(cookie.Domain, ".")
		if !IsHHHost(host) {
			continue
		}
		origin := &url.URL{Scheme: "https", Host: host, Path: cookie.Path}
		converted := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path, Secure: cookie.Secure, HttpOnly: cookie.HTTPOnly}
		if cookie.Expires != nil {
			converted.Expires = *cookie.Expires
		}
		jar.SetCookies(origin, []*http.Cookie{converted})
	}
	return browserHTTPClient(base, jar, http.DefaultTransport.(*http.Transport).Clone()), nil
}

func browserHTTPClient(base *url.URL, jar http.CookieJar, transport http.RoundTripper) *http.Client {
	return &http.Client{Jar: serializableCookieJar{jar}, Transport: browserCookieTransport{jar: jar, next: transport}, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Host != base.Host {
			return fmt.Errorf("refusing redirect outside selected HH origin to %s", req.URL.Redacted())
		}
		if strings.Contains(req.URL.Path, "/account/login") || strings.Contains(req.URL.Path, "/account/signup") {
			return fmt.Errorf("hh.ru authentication redirect; stop and sign in to the selected Chrome profile")
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many HH redirects; stopped")
		}
		return nil
	}}
}

// net/http's automatic Cookie.String serialization corrupts Chrome's literal
// JSON filter values. Keep its normal response-cookie lifecycle and XSRF access,
// but serialize the scoped jar's original values at the transport boundary.
type serializableCookieJar struct{ http.CookieJar }

func (jar serializableCookieJar) Cookies(u *url.URL) []*http.Cookie {
	var out []*http.Cookie
	for _, cookie := range jar.CookieJar.Cookies(u) {
		if cookie.Valid() == nil {
			out = append(out, cookie)
		}
	}
	return out
}

type browserCookieTransport struct {
	jar  http.CookieJar
	next http.RoundTripper
}

func (transport browserCookieTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var values []string
	for _, cookie := range transport.jar.Cookies(request.URL) {
		if (&http.Cookie{Name: cookie.Name, Value: "safe"}).Valid() != nil {
			return nil, fmt.Errorf("invalid browser cookie name; no request sent")
		}
		for _, b := range []byte(cookie.Value) {
			if b < 0x20 || b == 0x7f || b == ';' {
				return nil, fmt.Errorf("unsafe value for browser cookie %q; no request sent", cookie.Name)
			}
		}
		value := cookie.Value
		if cookie.Quoted {
			value = `"` + value + `"`
		}
		values = append(values, cookie.Name+"="+value)
	}
	clone := request.Clone(request.Context())
	clone.Header.Del("Cookie")
	if len(values) > 0 {
		clone.Header.Set("Cookie", strings.Join(values, "; "))
	}
	return transport.next.RoundTrip(clone)
}
