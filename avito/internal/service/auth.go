package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

type CookieReader func(context.Context, sweetcookie.Options) (sweetcookie.Result, error)

type AuthStore struct{ Path string }

const maxAuthBytes = 256 << 10

func DefaultAuthStore() (AuthStore, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return AuthStore{}, fmt.Errorf("locate auth store: %w", err)
	}
	return AuthStore{Path: filepath.Join(config, "avito", "auth.json")}, nil
}

type storedCookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Domain   string     `json:"domain"`
	Path     string     `json:"path"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure"`
	HTTPOnly bool       `json:"http_only"`
}

type authRecord struct {
	Version    int            `json:"version"`
	Browser    string         `json:"browser"`
	Profile    string         `json:"browser_profile,omitempty"`
	ImportedAt time.Time      `json:"imported_at"`
	Cookies    []storedCookie `json:"cookies"`
}

type AuthStatus struct {
	Store              string     `json:"store"`
	Imported           bool       `json:"imported"`
	Browser            string     `json:"browser,omitempty"`
	Profile            string     `json:"browser_profile,omitempty"`
	ImportedAt         *time.Time `json:"imported_at,omitempty"`
	CookieCount        int        `json:"cookie_count"`
	UsableCookieCount  int        `json:"usable_cookie_count"`
	ExpiredCookieCount int        `json:"expired_cookie_count"`
	CookieNames        []string   `json:"cookie_names"`
	NextExpiry         *time.Time `json:"next_expiry,omitempty"`
	LoginVerified      bool       `json:"login_verified"`
	IsAuthenticated    *bool      `json:"is_authenticated,omitempty"`
	Account            *Account   `json:"account,omitempty"`
	CheckedAt          *time.Time `json:"checked_at,omitempty"`
	HTTPRequests       int        `json:"http_requests"`
	Error              string     `json:"error,omitempty"`
	RetryAfter         string     `json:"retry_after,omitempty"`
	Warnings           []string   `json:"warnings,omitempty"`
}

func (store AuthStore) Import(ctx context.Context, browser Browser, profile string, read CookieReader) (*AuthStatus, error) {
	record, summary, err := store.prepareImport(ctx, browser, profile, read)
	if err != nil {
		return nil, err
	}
	if err := store.save(ctx, record); err != nil {
		return nil, err
	}
	summary.Imported = true
	return summary, nil
}

func (store AuthStore) prepareImport(ctx context.Context, browser Browser, profile string, read CookieReader) (*authRecord, *AuthStatus, error) {
	if _, err := NewBrowserFromValue(browser.String()); err != nil {
		return nil, nil, err
	}
	if read == nil {
		return nil, nil, fmt.Errorf("cookie reader must not be nil")
	}
	if store.Path == "" {
		return nil, nil, fmt.Errorf("auth store path must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	opts := sweetcookie.Options{
		URL: WebOrigin + "/", Origins: []string{WebOrigin + "/web/1/js/items", WebOrigin + authProfilesPath, WebOrigin + locationsPath, WebOrigin + filterSuggestPath},
		Browsers: []sweetcookie.Browser{sweetcookie.Browser(browser.String())},
		Mode:     sweetcookie.ModeFirst, Timeout: 30 * time.Second,
	}
	if profile != "" {
		opts.Profiles = map[sweetcookie.Browser]string{sweetcookie.Browser(browser.String()): profile}
	}
	result, err := read(ctx, opts)
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err != nil {
		// Browser helper diagnostics may contain credential data.
		return nil, nil, fmt.Errorf("could not import your %s session; check the selected browser profile and allow access to your OS Keychain/keyring", browser.String())
	}
	record := authRecord{Version: 1, Browser: browser.String(), Profile: profile, ImportedAt: time.Now().UTC()}
	for _, cookie := range result.Cookies {
		path := cookie.Path
		if path == "" {
			path = "/"
		}
		stored := storedCookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: path,
			Expires: cookie.Expires, Secure: cookie.Secure, HTTPOnly: cookie.HTTPOnly}
		if !stored.valid() || stored.expired(record.ImportedAt) || !supportedCookiePath(path) {
			continue
		}
		record.Cookies = append(record.Cookies, stored)
		if record.Profile == "" {
			record.Profile = cookie.Source.Profile
		}
	}
	if len(record.Cookies) == 0 {
		return nil, nil, fmt.Errorf("no Avito session found in %s; open avito.ru in that browser, sign in, then re-import; use --browser-profile for a different profile", browser.String())
	}
	summary := record.summary(store.Path, record.ImportedAt)
	summary.Imported = false
	if len(result.Warnings) > 0 {
		summary.Warnings = append(summary.Warnings, fmt.Sprintf("Browser cookie reader reported %d warning(s); only valid Avito cookies were imported.", len(result.Warnings)))
	}
	return &record, summary, nil
}

func (store AuthStore) save(ctx context.Context, record *authRecord) error {
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxAuthBytes {
		return fmt.Errorf("could not encode imported cookies within the auth store size limit")
	}
	dir := filepath.Dir(store.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("auth directory %q must be a directory, not a symlink", dir)
	}
	if err := os.Chmod(dir, 0700); err != nil { //nolint:gosec // G302: the owner needs execute permission to traverse this private directory.
		return fmt.Errorf("restrict auth directory permissions: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAtomic(store.Path, data); err != nil {
		return fmt.Errorf("save imported cookies: %w", err)
	}
	return nil
}

func (store AuthStore) Status() (*AuthStatus, error) {
	record, err := store.load()
	if errors.Is(err, os.ErrNotExist) {
		return &AuthStatus{Store: store.Path, CookieNames: []string{}, Warnings: []string{"No saved cookies; run avito auth import --browser chrome (or your browser)."}}, nil
	}
	if err != nil {
		return nil, err
	}
	return record.summary(store.Path, time.Now()), nil
}

func (record authRecord) summary(path string, now time.Time) *AuthStatus {
	result := &AuthStatus{Store: path, Imported: true, Browser: record.Browser, Profile: record.Profile,
		ImportedAt: &record.ImportedAt, CookieCount: len(record.Cookies), CookieNames: []string{}}
	for _, cookie := range record.Cookies {
		result.CookieNames = append(result.CookieNames, cookie.Name)
		if cookie.expired(now) {
			result.ExpiredCookieCount++
			continue
		}
		result.UsableCookieCount++
		if cookie.Expires != nil && (result.NextExpiry == nil || cookie.Expires.Before(*result.NextExpiry)) {
			result.NextExpiry = cookie.Expires
		}
	}
	slices.Sort(result.CookieNames)
	result.CookieNames = slices.Compact(result.CookieNames)
	if result.UsableCookieCount == 0 {
		result.Warnings = append(result.Warnings, "All saved cookies have expired; run avito auth import again.")
	}
	return result
}

func (store AuthStore) load() (*authRecord, error) {
	if store.Path == "" {
		return nil, fmt.Errorf("auth store path must not be empty")
	}
	info, err := os.Lstat(store.Path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("auth store %q must be a regular file readable only by its owner; re-import with avito auth import", store.Path)
	}
	f, err := os.Open(store.Path)
	if err != nil {
		return nil, fmt.Errorf("open auth store: %w", err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(f, maxAuthBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read auth store: %w", err)
	}
	var record authRecord
	invalid := func() (*authRecord, error) {
		return nil, fmt.Errorf("invalid auth store %q; run avito auth import again", store.Path)
	}
	if len(data) > maxAuthBytes || json.Unmarshal(data, &record) != nil || record.Version != 1 || record.ImportedAt.IsZero() || len(record.Cookies) == 0 {
		return invalid()
	}
	if _, err := NewBrowserFromValue(record.Browser); err != nil {
		return invalid()
	}
	for _, cookie := range record.Cookies {
		if !cookie.valid() || !supportedCookiePath(cookie.Path) {
			return invalid()
		}
	}
	return &record, nil
}

func (store AuthStore) cookiesFor(target *url.URL) ([]*http.Cookie, error) {
	if target == nil || target.Scheme != "https" || target.Host != "www.avito.ru" || target.User != nil {
		return nil, fmt.Errorf("saved cookies require an HTTPS www.avito.ru target")
	}
	record, err := store.load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no saved Avito cookies; run avito auth import --browser chrome (or your browser)")
	}
	if err != nil {
		return nil, err
	}
	return record.cookiesFor(target)
}

func (record authRecord) cookiesFor(target *url.URL) ([]*http.Cookie, error) {
	if target == nil || target.Scheme != "https" || target.Host != "www.avito.ru" || target.User != nil {
		return nil, fmt.Errorf("saved cookies require an HTTPS www.avito.ru target")
	}
	var cookies []*http.Cookie
	now := time.Now()
	for _, stored := range record.Cookies {
		if !stored.expired(now) && pathMatches(target.Path, stored.Path) {
			cookies = append(cookies, &http.Cookie{Name: stored.Name, Value: stored.Value, Path: stored.Path}) //nolint:gosec // G124: outbound request cookies only serialize name/value after HTTPS validation.
		}
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("no unexpired saved Avito cookies match this request; run avito auth import again")
	}
	// RFC 6265: more specific paths precede root cookies with the same name.
	slices.SortStableFunc(cookies, func(a, b *http.Cookie) int { return len(b.Path) - len(a.Path) })
	return cookies, nil
}

func (cookie storedCookie) valid() bool {
	domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
	if (domain != "avito.ru" && domain != "www.avito.ru") || cookie.Value == "" || !strings.HasPrefix(cookie.Path, "/") {
		return false
	}
	check := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path} //nolint:gosec // G124: this value is only used to validate imported cookie syntax.
	if check.Valid() != nil {
		return false
	}
	if cookie.Expires != nil {
		if _, err := cookie.Expires.MarshalJSON(); err != nil {
			return false
		}
	}
	return true
}

func (cookie storedCookie) expired(now time.Time) bool {
	return cookie.Expires != nil && !cookie.Expires.After(now)
}

func pathMatches(requestPath, cookiePath string) bool {
	return requestPath == cookiePath || (strings.HasPrefix(requestPath, cookiePath) &&
		(strings.HasSuffix(cookiePath, "/") || strings.HasPrefix(strings.TrimPrefix(requestPath, cookiePath), "/")))
}

func supportedCookiePath(path string) bool {
	return pathMatches("/web/1/js/items", path) || pathMatches(authProfilesPath, path) || pathMatches(locationsPath, path) || pathMatches(filterSuggestPath, path)
}
