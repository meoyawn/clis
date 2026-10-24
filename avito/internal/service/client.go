package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultWebUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

type Client struct {
	http *http.Client
	auth AuthStore
}

func NewClient(httpClient *http.Client, auth AuthStore) (*Client, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client must not be nil")
	}
	return &Client{http: httpClient, auth: auth}, nil
}

func NewHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type PreparedRequest struct {
	request *http.Request
}

func (p *PreparedRequest) sourceURL() string {
	return p.request.URL.String()
}

func (c *Client) Prepare(ctx context.Context, target *url.URL, options RequestOptions, isHTML bool) (*PreparedRequest, error) {
	return c.prepare(ctx, target, options, isHTML, c.auth.cookiesFor)
}

func (c *Client) prepare(ctx context.Context, target *url.URL, options RequestOptions, isHTML bool, cookiesFor func(*url.URL) ([]*http.Cookie, error)) (*PreparedRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if target == nil || target.User != nil || target.Scheme != "https" || target.Host != "www.avito.ru" {
		return nil, fmt.Errorf("request must target HTTPS www.avito.ru without URL credentials")
	}
	userAgent := options.UserAgent
	if userAgent == "" {
		userAgent = DefaultWebUserAgent
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("user-agent must contain no newlines")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if isHTML {
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
	} else {
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("X-Source", "client-browser")
	}
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Referer", WebOrigin+"/")
	cookies, err := cookiesFor(target)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")
	return &PreparedRequest{request: req}, nil
}

type Exchange struct {
	Body    []byte
	Headers http.Header
	Status  int
}

type HTTPError struct {
	Status     int
	Message    string
	RetryAfter string
}

func (e *HTTPError) Error() string {
	message := fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	if e.RetryAfter != "" {
		message += "; Retry-After: " + e.RetryAfter
	}
	return message
}

func (c *Client) Do(prepared *PreparedRequest) (*Exchange, error) {
	response, err := c.http.Do(prepared.request)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck
	const maxBytes = 16 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	exchange := &Exchange{Body: body, Headers: response.Header.Clone(), Status: response.StatusCode}
	// Preserve the server's status and retry hint even if its error body is
	// truncated, oversized, or unreadable.
	if response.StatusCode != http.StatusOK {
		message := http.StatusText(response.StatusCode)
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Restricted *struct {
				Message string `json:"message"`
			} `json:"too-many-requests"`
		}
		_ = json.Unmarshal(body, &payload) //nolint:errcheck
		if payload.Error.Message != "" {
			message = payload.Error.Message
		}
		if payload.Restricted != nil {
			message = payload.Restricted.Message
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			message = "redirect not followed; use the current canonical listing URL"
		}
		return exchange, &HTTPError{Status: response.StatusCode, Message: truncate(message, 500), RetryAfter: response.Header.Get("Retry-After")}
	}
	if err != nil {
		return exchange, fmt.Errorf("read HTTP response: %w", err)
	}
	if len(body) > maxBytes {
		return exchange, fmt.Errorf("response exceeds %d bytes", maxBytes)
	}
	if response.Header.Get("X-Firewall-Show-Captcha") == "true" {
		return exchange, &HTTPError{Status: response.StatusCode, Message: "Avito requested a browser challenge", RetryAfter: response.Header.Get("Retry-After")}
	}
	return exchange, nil
}

func rateHeaders(headers http.Header) map[string]string {
	result := map[string]string{}
	for _, name := range []string{"Retry-After", "X-RateLimit-Limit", "X-RateLimit-Remaining", "X-RateLimit-Reset", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "RateLimit", "RateLimit-Policy"} {
		if value := headers.Get(name); value != "" {
			result[name] = value
		}
	}
	return result
}

func truncate(value string, length int) string {
	chars := []rune(value)
	if len(chars) > length {
		return string(chars[:length]) + "…"
	}
	return value
}
