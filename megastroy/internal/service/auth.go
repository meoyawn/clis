package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

type Browser struct{ value string }

const (
	chromeBrowser  = "chrome"
	firefoxBrowser = "firefox"
	braveBrowser   = "brave"
	edgeBrowser    = "edge"
	safariBrowser  = "safari"
)

var browserValues = [...]string{chromeBrowser, firefoxBrowser, braveBrowser, edgeBrowser, safariBrowser}

func Chrome() Browser            { return Browser{chromeBrowser} }
func Firefox() Browser           { return Browser{firefoxBrowser} }
func Brave() Browser             { return Browser{braveBrowser} }
func Edge() Browser              { return Browser{edgeBrowser} }
func Safari() Browser            { return Browser{safariBrowser} }
func (b Browser) String() string { return b.value }
func NewBrowserFromValue(value string) (Browser, error) {
	switch value {
	case chromeBrowser:
		return Chrome(), nil
	case firefoxBrowser:
		return Firefox(), nil
	case braveBrowser:
		return Brave(), nil
	case edgeBrowser:
		return Edge(), nil
	case safariBrowser:
		return Safari(), nil
	default:
		return Browser{}, fmt.Errorf("invalid browser %q; allowed values: %v", value, browserValues)
	}
}

var anonymousCookieNames = []string{"__ddg1_", "__ddg2_", "__ddg5_", "__ddg8_", "__ddg9_", "__ddg10_", "__ddgid_", "__ddgmark_"}

const maxSessionBytes = 64 << 10

type Cookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Domain   string     `json:"domain"`
	Path     string     `json:"path"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure"`
	HTTPOnly bool       `json:"http_only"`
}

type Session struct {
	Version      int       `json:"version"`
	Browser      string    `json:"browser"`
	Profile      string    `json:"browser_profile,omitempty"`
	CookieSource string    `json:"cookie_source,omitempty"`
	ImportedAt   time.Time `json:"imported_at"`
	UserAgent    string    `json:"user_agent,omitempty"`
	Cookies      []Cookie  `json:"cookies"`
}

func (c Cookie) valid() bool {
	domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
	if !slices.Contains(anonymousCookieNames, c.Name) || (domain != "megastroy.com" && domain != "kazan.megastroy.com") || c.Path != "/" || c.Value == "" {
		return false
	}
	h := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path}
	return h.Valid() == nil
}
func (c Cookie) expired(now time.Time) bool { return c.Expires != nil && !c.Expires.After(now) }

func (s Session) HTTPCookies(now time.Time) []*http.Cookie {
	var cookies []*http.Cookie
	for _, c := range s.Cookies {
		if !c.valid() || c.expired(now) {
			continue
		}
		h := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.Expires != nil {
			h.Expires = *c.Expires
		}
		cookies = append(cookies, h)
	}
	return cookies
}

func (s *Session) Merge(incoming []*http.Cookie, now time.Time) {
	for _, h := range incoming {
		c := Cookie{Name: h.Name, Value: h.Value, Domain: h.Domain, Path: h.Path, Secure: h.Secure, HTTPOnly: h.HttpOnly}
		if c.Domain == "" {
			c.Domain = "kazan.megastroy.com"
		}
		if c.Path == "" {
			c.Path = "/"
		}
		if !slices.Contains(anonymousCookieNames, c.Name) {
			continue
		}
		if h.MaxAge > 0 {
			expires := now.Add(time.Duration(h.MaxAge) * time.Second)
			c.Expires = &expires
		} else if !h.Expires.IsZero() {
			expires := h.Expires
			c.Expires = &expires
		}
		// A deletion may have an empty value, but must still match a permitted
		// name, domain and path before it can remove a saved cookie.
		check := c
		if check.Value == "" {
			check.Value = "deleted"
		}
		if !check.valid() {
			continue
		}
		s.Cookies = slices.DeleteFunc(s.Cookies, func(old Cookie) bool {
			return old.Name == c.Name && strings.TrimPrefix(old.Domain, ".") == strings.TrimPrefix(c.Domain, ".") && old.Path == c.Path
		})
		if h.MaxAge >= 0 && c.valid() && !c.expired(now) {
			s.Cookies = append(s.Cookies, c)
		}
	}
}

type AuthStore struct{ Path string }

func DefaultAuthStore() (AuthStore, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return AuthStore{}, fmt.Errorf("locate config directory: %w", err)
	}
	return AuthStore{Path: filepath.Join(dir, "megastroy", "auth.json")}, nil
}

func (a AuthStore) Load() (Session, error) {
	info, err := os.Lstat(a.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, fmt.Errorf("inspect auth store: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Session{}, fmt.Errorf("auth store %q must be a regular owner-only file; re-import with megastroy auth import --browser chrome", a.Path)
	}
	f, err := os.Open(a.Path)
	if err != nil {
		return Session{}, fmt.Errorf("open auth store: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	if err != nil {
		return Session{}, fmt.Errorf("read auth store: %w", err)
	}
	var session Session
	if len(b) > maxSessionBytes || json.Unmarshal(b, &session) != nil || session.Version != 1 || session.ImportedAt.IsZero() || len(session.Cookies) == 0 || strings.ContainsAny(session.UserAgent, "\r\n") {
		return Session{}, fmt.Errorf("invalid auth store %q; re-import with megastroy auth import --browser chrome", a.Path)
	}
	for _, c := range session.Cookies {
		if !c.valid() {
			return Session{}, fmt.Errorf("auth store %q contains an unsupported cookie; re-import", a.Path)
		}
	}
	return session, nil
}

func (a AuthStore) Save(ctx context.Context, s Session) error {
	if a.Path == "" {
		return fmt.Errorf("auth store path must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Version != 1 || s.ImportedAt.IsZero() || len(s.Cookies) == 0 || strings.ContainsAny(s.UserAgent, "\r\n") {
		return fmt.Errorf("refusing to save an invalid anonymous session")
	}
	for _, c := range s.Cookies {
		if !c.valid() {
			return fmt.Errorf("refusing to save an unsupported cookie")
		}
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) > maxSessionBytes {
		return fmt.Errorf("session exceeds the supported encoding or size limit")
	}
	dir := filepath.Dir(a.Path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("auth directory %q must be a directory, not a symlink", dir)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("restrict auth directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".auth-*")
	if err != nil {
		return fmt.Errorf("create auth temporary file: %w", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(b)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write auth store: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), a.Path); err != nil {
		return fmt.Errorf("replace auth store: %w", err)
	}
	return nil
}

func (a AuthStore) Import(ctx context.Context, browser Browser, profile, userAgent string) (*AuthStatus, error) {
	if _, err := NewBrowserFromValue(browser.String()); err != nil {
		return nil, err
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("user-agent must not contain newlines")
	}
	opts := sweetcookie.Options{
		URL: Origin + "/", Names: slices.Clone(anonymousCookieNames), Browsers: []sweetcookie.Browser{sweetcookie.Browser(browser.String())}, Mode: sweetcookie.ModeFirst, Timeout: 30 * time.Second,
	}
	if profile != "" {
		opts.Profiles = map[sweetcookie.Browser]string{sweetcookie.Browser(browser.String()): profile}
	}
	result, err := sweetcookie.Get(ctx, opts)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("could not read %s cookies; check --browser-profile and allow OS Keychain/keyring access", browser.String())
	}
	session, err := sessionFromBrowser(result, browser, profile, userAgent, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err = a.Save(ctx, session); err != nil {
		return nil, err
	}
	status := session.status(a.Path, time.Now())
	if len(result.Warnings) > 0 {
		status.Warnings = append(status.Warnings, fmt.Sprintf("Cookie reader reported %d warning(s); only valid anonymous cookies were saved", len(result.Warnings)))
	}
	return status, nil
}

func sessionFromBrowser(result sweetcookie.Result, browser Browser, profile, userAgent string, now time.Time) (Session, error) {
	s := Session{Version: 1, Browser: browser.String(), Profile: profile, ImportedAt: now, UserAgent: userAgent}
	for _, c := range result.Cookies {
		p := c.Path
		if p == "" {
			p = "/"
		}
		stored := Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: p, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly}
		if !stored.valid() || stored.expired(now) {
			continue
		}
		if source := c.Source.StorePath; source != "" {
			if s.CookieSource != "" && s.CookieSource != source {
				return Session{}, fmt.Errorf("cookies came from multiple %s profiles; select one with --browser-profile", browser.String())
			}
			s.CookieSource = source
		}
		if s.Profile == "" {
			s.Profile = c.Source.Profile
		}
		s.Cookies = append(s.Cookies, stored)
	}
	if len(s.Cookies) == 0 {
		return Session{}, fmt.Errorf("no unexpired DDoS-Guard cookies found in %s; open %s in that browser until the catalog appears, then re-import; use --browser-profile if needed", browser.String(), Origin)
	}
	return s, nil
}

type AuthStatus struct {
	Store             string     `json:"store"`
	Browser           string     `json:"browser,omitempty"`
	Profile           string     `json:"browser_profile,omitempty"`
	Imported          bool       `json:"imported"`
	CookieCount       int        `json:"cookie_count"`
	UsableCookieCount int        `json:"usable_cookie_count"`
	CookieNames       []string   `json:"cookie_names"`
	NextExpiry        *time.Time `json:"next_expiry"`
	Warnings          []string   `json:"warnings,omitempty"`
}

func (s Session) status(path string, now time.Time) *AuthStatus {
	status := &AuthStatus{Store: path, Browser: s.Browser, Profile: s.Profile, Imported: s.Version == 1, CookieCount: len(s.Cookies), CookieNames: []string{}}
	for _, c := range s.Cookies {
		status.CookieNames = appendUnique(status.CookieNames, c.Name)
		if !c.valid() || c.expired(now) {
			continue
		}
		status.UsableCookieCount++
		if c.Expires != nil && (status.NextExpiry == nil || c.Expires.Before(*status.NextExpiry)) {
			status.NextExpiry = c.Expires
		}
	}
	slices.Sort(status.CookieNames)
	if status.UsableCookieCount == 0 {
		status.Warnings = append(status.Warnings, "No usable saved cookies; run megastroy auth import --browser chrome")
	}
	return status
}

func (a AuthStore) Status() (*AuthStatus, error) {
	s, err := a.Load()
	if err != nil {
		return nil, err
	}
	return s.status(a.Path, time.Now()), nil
}
