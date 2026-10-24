package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const WebOrigin = "https://www.ozon.ru"
const entrypointPath = "/api/entrypoint-api.bx/page/json/v2"
const DefaultWebUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

type Client struct {
	http *http.Client
	auth AuthStore
}
type RequestOptions struct{ UserAgent string }

func NewClient(h *http.Client, auth AuthStore) (*Client, error) {
	if h == nil {
		return nil, fmt.Errorf("HTTP client must not be nil")
	}
	// Enforce the request bound even with an injected client.
	owned := *h
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	owned.Jar = nil
	return &Client{http: &owned, auth: auth}, nil
}

func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives: true,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type HTTPError struct {
	Status     int
	RetryAfter string
}

type browserChallengeError struct{ *HTTPError }

func (e *browserChallengeError) Error() string {
	return "Ozon returned a browser challenge (HTTP 403); refresh ozon.ru in your browser, then run ozon auth import again"
}

func (e *browserChallengeError) Unwrap() error { return e.HTTPError }

// Keep redirect targets out of diagnostics: their queries may contain session state.
type pageRedirectError struct {
	target  string
	cause   error
	cookies []*http.Cookie
}

func (e *pageRedirectError) Error() string { return e.cause.Error() }
func (e *pageRedirectError) Unwrap() error { return e.cause }

func (e *HTTPError) Error() string {
	s := fmt.Sprintf("Ozon returned HTTP %d (%s)", e.Status, http.StatusText(e.Status))
	if e.Status == 401 || e.Status == 403 {
		s += "; Ozon rejected this HTTP session; this does not establish whether your browser is signed in"
	}
	if e.Status >= 300 && e.Status < 400 {
		s += "; redirect not followed"
	}
	if e.RetryAfter != "" {
		s += "; Retry-After: " + e.RetryAfter
	}
	return s
}

type page struct {
	Layout       []layoutWidget             `json:"layout"`
	WidgetStates map[string]json.RawMessage `json:"widgetStates"`
	NextPage     string                     `json:"nextPage"`
	PageInfo     struct {
		URL      string `json:"url"`
		PageType string `json:"pageType"`
	} `json:"pageInfo"`
	UserInfo struct {
		User struct {
			ID        json.Number `json:"userId"`
			FirstName string      `json:"firstName"`
			LastName  string      `json:"lastName"`
			LoggedIn  *bool       `json:"isLoggedIn"`
		} `json:"user"`
	} `json:"userInfo"`
	Redirect string `json:"redirect"`
	meta     Metadata
}

type layoutWidget struct {
	Component    string `json:"component"`
	Name         string `json:"name"`
	StateID      string `json:"stateId"`
	Placeholders []struct {
		Widgets []layoutWidget `json:"widgets"`
	} `json:"placeholders"`
}

func entrypointURL(path string) *url.URL {
	target, err := url.Parse(WebOrigin + entrypointPath)
	if err != nil {
		panic(fmt.Errorf("parse Ozon API endpoint: %w", err))
	}
	target.RawQuery = url.Values{"url": {path}}.Encode()
	return target
}

func (c *Client) fetchPage(ctx context.Context, relative string, target *url.URL, opt RequestOptions, cookiesFor func(*url.URL) ([]*http.Cookie, error)) (*page, error) {
	response, err := c.fetchJSON(ctx, relative, target, opt, cookiesFor, nil)
	if response == nil {
		return nil, err
	}
	p := page{meta: response.meta}
	if err != nil {
		return &p, err
	}
	return decodePageResponse(response)
}

func decodePageResponse(response *jsonResponse) (*page, error) {
	p := page{meta: response.meta}
	if json.Unmarshal(response.body, &p) != nil {
		return &p, fmt.Errorf("Ozon returned non-page JSON or a browser challenge; reopen ozon.ru and re-import your session")
	}
	if p.Redirect != "" {
		return &p, &pageRedirectError{target: p.Redirect, cause: fmt.Errorf("Ozon redirected the page; redirect not followed"), cookies: response.cookies}
	}
	if len(p.WidgetStates) == 0 {
		return &p, fmt.Errorf("Ozon page has no widget states; the page may be unavailable or its schema has changed")
	}
	p.meta.IsAuthenticated = p.UserInfo.User.LoggedIn
	if p.meta.IsAuthenticated != nil && !*p.meta.IsAuthenticated {
		p.meta.Warnings = append(p.meta.Warnings, "Ozon returned a signed-out page; re-import your browser session for account-specific prices and delivery.")
	}
	return &p, nil
}

// Both page reads and cart writes use the same bounded, credential-filtering transport.
// A non-nil payload sends one POST; this transport does not follow redirects or retry.
type jsonResponse struct {
	body    []byte
	meta    Metadata
	cookies []*http.Cookie
}

func (c *Client) fetchJSON(ctx context.Context, relative string, target *url.URL, opt RequestOptions, cookiesFor func(*url.URL) ([]*http.Cookie, error), payload []byte) (*jsonResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cookies, err := cookiesFor(target)
	if err != nil {
		return nil, err
	}
	ua := opt.UserAgent
	if ua == "" {
		ua = DefaultWebUserAgent
	}
	if strings.ContainsAny(ua, "\r\n") {
		return nil, fmt.Errorf("user-agent must contain no newlines")
	}
	method := http.MethodGet
	var reader io.Reader
	if payload != nil {
		method, reader = http.MethodPost, bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Referer", WebOrigin+relative)
	req.Header.Set("X-O3-App-Name", "dweb_client")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", WebOrigin)
	}
	for _, cookie := range cookies {
		if !renewalCookie(cookie.Name) {
			req.AddCookie(cookie)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &jsonResponse{meta: Metadata{HTTPRequests: 1, FetchedAt: time.Now().UTC(), SourceURLs: []string{WebOrigin + relative}}}
	resp, err := c.http.Do(req)
	if err != nil {
		return result, fmt.Errorf("Ozon request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // Read errors are checked below; closing cannot recover the response.
	if resp.StatusCode != http.StatusOK {
		failure := &HTTPError{Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
		if resp.StatusCode == http.StatusForbidden {
			var challenge struct {
				URL string `json:"challengeURL"`
			}
			if json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&challenge) == nil {
				u, err := url.Parse(challenge.URL)
				if err == nil && u.Scheme == "https" && u.Host == "www.ozon.ru" && u.User == nil && u.Path == "/challenge.html" {
					return result, &browserChallengeError{failure}
				}
			}
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			if target := resp.Header.Get("Location"); target != "" {
				return result, &pageRedirectError{target: target, cause: failure, cookies: navigationCookies(resp.Cookies())}
			}
		}
		return result, failure
	}
	const maxBytes = 32 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return result, fmt.Errorf("read Ozon response: %w", err)
	}
	if len(body) > maxBytes {
		return result, fmt.Errorf("Ozon response exceeds %d bytes", maxBytes)
	}
	if v := resp.Header.Get("Retry-After"); v != "" {
		result.meta.RetryAfter = v
	}
	result.body, result.cookies = body, navigationCookies(resp.Cookies())
	return result, nil
}

// Only relative paths and the Ozon web origin are accepted. Page URLs are
// carried inside entrypoint's url parameter and must not become open fetches.
func safePageURL(raw string) (string, error) {
	if len(raw) > 16384 {
		return "", fmt.Errorf("page URL exceeds 16384 bytes")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.User != nil || u.Opaque != "" || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") {
		return "", fmt.Errorf("invalid Ozon page URL")
	}
	if u.IsAbs() && (u.Scheme != "https" || (u.Host != "www.ozon.ru" && u.Host != "ozon.ru")) {
		return "", fmt.Errorf("page URL must use https://www.ozon.ru")
	}
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "", fmt.Errorf("page URL must have an absolute Ozon path")
	}
	u.Scheme = ""
	u.Host = ""
	u.Fragment = ""
	return u.String(), nil
}

// Redirects may update anti-bot/navigation state, but must not replace the
// imported account credentials or hand renewal credentials to the next request.
func navigationCookies(cookies []*http.Cookie) []*http.Cookie {
	return slices.DeleteFunc(cookies, func(cookie *http.Cookie) bool {
		return renewalCookie(cookie.Name) || cookie.Name == "__Secure-access-token" || cookie.Name == "__Secure-user-id"
	})
}
