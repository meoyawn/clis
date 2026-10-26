package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

type Session struct {
	Profile    string         `json:"profile"`
	ImportedAt time.Time      `json:"imported_at"`
	Headers    http.Header    `json:"headers"`
	Cookies    []*http.Cookie `json:"cookies"`
}

func ChromeHeaders(ctx context.Context) (http.Header, error) {
	if runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("automatic Chrome headers require macOS; supply auth login --headers-file from the selected Chrome session")
	}
	data, err := exec.CommandContext(ctx, "/usr/bin/defaults", "read", "/Applications/Google Chrome.app/Contents/Info", "CFBundleShortVersionString").Output()
	if err != nil {
		return nil, fmt.Errorf("read installed Chrome version: %w; supply --headers-file", err)
	}
	major := regexp.MustCompile(`^\d+`).FindString(strings.TrimSpace(string(data)))
	if major == "" {
		return nil, fmt.Errorf("installed Chrome version is invalid; supply --headers-file")
	}
	h := http.Header{}
	h.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/"+major+".0.0.0 Safari/537.36")
	h.Set("Sec-CH-UA", `"Google Chrome";v="`+major+`", "Chromium";v="`+major+`", "Not(A:Brand";v="24"`)
	h.Set("Sec-CH-UA-Mobile", "?0")
	h.Set("Sec-CH-UA-Platform", `"macOS"`)
	return h, nil
}

func ImportSession(ctx context.Context, profile, headersFile string) (*Session, error) {
	if strings.TrimSpace(profile) == "" {
		return nil, fmt.Errorf("invalid Chrome profile %q", profile)
	}
	var headers http.Header
	var err error
	if headersFile == "" {
		headers, err = ChromeHeaders(ctx)
	} else {
		data, readErr := os.ReadFile(headersFile)
		if readErr != nil {
			return nil, readErr
		}
		var values map[string]string
		if err = json.Unmarshal(data, &values); err != nil {
			return nil, fmt.Errorf("decode headers file: %w", err)
		}
		headers = http.Header{}
		for k, v := range values {
			switch strings.ToLower(k) {
			case "user-agent", "accept-language", "dnt", "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform", "sec-ch-ua-arch", "sec-ch-ua-bitness", "sec-ch-ua-full-version", "sec-ch-ua-full-version-list", "sec-ch-ua-model", "sec-ch-ua-platform-version":
				if strings.ContainsAny(v, "\r\n") {
					return nil, fmt.Errorf("invalid header %q", k)
				}
				headers.Set(k, v)
			default:
				return nil, fmt.Errorf("header %q is not allowed in a browser header snapshot", k)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if headers.Get("User-Agent") == "" {
		return nil, fmt.Errorf("headers file must include the selected Chrome User-Agent")
	}
	return importSession(ctx, profile, headers)
}

func importSession(ctx context.Context, profile string, headers http.Header) (*Session, error) {
	if strings.TrimSpace(profile) == "" {
		return nil, fmt.Errorf("invalid Chrome profile %q; run auth login to select it explicitly", profile)
	}
	result, err := sweetcookie.Get(ctx, sweetcookie.Options{URL: Origin + "/", Browsers: []sweetcookie.Browser{sweetcookie.BrowserChrome}, Profiles: map[sweetcookie.Browser]string{sweetcookie.BrowserChrome: profile}, Timeout: 15 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("read selected Chrome cookies with SweetCookie: %w", err)
	}
	s := &Session{Profile: profile, ImportedAt: time.Now().UTC(), Headers: headers.Clone()}
	for _, c := range result.Cookies {
		if host := strings.TrimPrefix(c.Domain, "."); host != "levels.fyi" && host != "api.levels.fyi" && host != "www.levels.fyi" {
			continue
		}
		if (&http.Cookie{Name: c.Name, Value: "safe"}).Valid() != nil || strings.ContainsAny(c.Value, "\r\n;") {
			return nil, fmt.Errorf("unsafe cookie characters; stopped")
		}
		cookie := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.Expires != nil {
			cookie.Expires = *c.Expires
		}
		s.Cookies = append(s.Cookies, cookie)
	}
	if len(s.Cookies) == 0 {
		return nil, fmt.Errorf("no Levels.fyi cookies in Chrome profile %q; sign in there before importing", profile)
	}
	if err := Authorize(s); err != nil {
		return nil, err
	}
	return s, nil
}

func SaveSession(path string, s *Session) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func LoadSession(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Session
	if err = json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("invalid saved session; import again with auth login")
	}
	if s.Headers.Get("User-Agent") == "" || len(s.Cookies) == 0 {
		return nil, fmt.Errorf("saved session has no browser headers or cookies; import again")
	}
	return &s, nil
}

// LoadAuthorizedSession prepares credentials before the first API request.
func LoadAuthorizedSession(ctx context.Context, path string) (*Session, error) {
	s, err := LoadSession(path)
	if err != nil {
		return nil, err
	}
	if err = Authorize(s); err == nil {
		return s, nil
	}
	if !errors.Is(err, errExpiredToken) {
		return nil, err
	}
	// Resync the already-selected local browser store once, before any HTTP.
	// This does not renew tokens over the network or retry a rejected request.
	fresh, err := importSession(ctx, s.Profile, s.Headers)
	if err != nil {
		return nil, fmt.Errorf("saved token expired and Chrome profile %q is not ready: %w; no API request sent", s.Profile, err)
	}
	if authUser(fresh) != authUser(s) {
		return nil, fmt.Errorf("account changed in Chrome profile %q; run auth login to select it explicitly; no API request sent", s.Profile)
	}
	if err = SaveSession(path, fresh); err != nil {
		return nil, fmt.Errorf("save resynced Chrome credentials: %w", err)
	}
	return fresh, nil
}

// Each invocation owns this transport. No background process or connection
// survives the command; the caller closes idle connections before returning.
func HTTPClient(s *Session) (*http.Client, http.Header, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConnsPerHost = 1
	transport.ForceAttemptHTTP2 = true
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Host != "api.levels.fyi" || strings.Contains(req.URL.Path, "login") || len(via) > 3 {
			return fmt.Errorf("levels access/authentication redirect; stopped")
		}
		return nil
	}}
	if s == nil {
		return client, http.Header{}, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, nil, err
	}
	u, _ := url.Parse(Origin)
	jar.SetCookies(u, s.Cookies)
	client.Transport = &sessionTransport{next: transport, jar: jar}
	return client, s.Headers.Clone(), nil
}

type sessionTransport struct {
	next *http.Transport
	jar  http.CookieJar
}

func (t *sessionTransport) CloseIdleConnections() { t.next.CloseIdleConnections() }
func (t *sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	var pairs []string
	for _, c := range t.jar.Cookies(req.URL) {
		if (&http.Cookie{Name: c.Name, Value: "safe"}).Valid() != nil || strings.ContainsAny(c.Value, "\r\n;") {
			return nil, fmt.Errorf("unsafe saved cookie; stopped")
		}
		value := c.Value
		if c.Quoted {
			value = `"` + value + `"`
		}
		pairs = append(pairs, c.Name+"="+value)
	}
	if len(pairs) > 0 {
		copy.Header.Set("Cookie", strings.Join(pairs, "; "))
	}
	res, err := t.next.RoundTrip(copy)
	if err == nil {
		t.jar.SetCookies(req.URL, res.Cookies())
	}
	return res, err
}
