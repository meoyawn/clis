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
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

const Origin = "https://ozon.tech"

var cookieNames = []string{"__Secure-ETC", "__Secure-ab-group", "abt_data"}

type savedCookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Domain   string     `json:"domain"`
	Path     string     `json:"path"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure"`
	HTTPOnly bool       `json:"http_only"`
}

type session struct {
	Profile    string        `json:"profile"`
	ImportedAt time.Time     `json:"imported_at"`
	Cookies    []savedCookie `json:"cookies"`
}

type AuthStatus struct {
	Path             string     `json:"path"`
	Imported         bool       `json:"imported"`
	SessionAvailable bool       `json:"cookies_available"`
	APIAvailable     *bool      `json:"api_available,omitempty"`
	Profile          string     `json:"profile,omitempty"`
	ImportedAt       *time.Time `json:"imported_at,omitempty"`
	CookieNames      []string   `json:"cookie_names"`
}

func SessionPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate Ozon Tech config directory: %w", err)
	}
	return filepath.Join(dir, "ozon-tech", "session.json"), nil
}

func validCookie(c savedCookie) bool {
	host := strings.TrimPrefix(c.Domain, ".")
	return host == "ozon.tech" && slices.Contains(cookieNames, c.Name) &&
		c.Value != "" && c.Path == "/" &&
		(&http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path}).Valid() == nil
}

// Only explicit import accesses Chrome, SweetCookie or OS Keychain.
func ImportSession(ctx context.Context, path, profile, cookieFile string) (AuthStatus, error) {
	if profile == "" {
		return AuthStatus{}, fmt.Errorf("invalid Chrome profile %q; use --browser-profile", profile)
	}
	opts := sweetcookie.Options{
		URL: Origin + "/", Names: slices.Clone(cookieNames),
		Browsers: []sweetcookie.Browser{sweetcookie.BrowserChrome},
		Profiles: map[sweetcookie.Browser]string{sweetcookie.BrowserChrome: profile},
		Mode:     sweetcookie.ModeFirst, Timeout: 15 * time.Second,
	}
	if cookieFile != "" {
		opts.Browsers = []sweetcookie.Browser{sweetcookie.BrowserInline}
		opts.Inline = sweetcookie.InlineCookies{File: cookieFile}
	}
	result, err := sweetcookie.Get(ctx, opts)
	if err != nil {
		return AuthStatus{}, fmt.Errorf("read Ozon Tech cookies through SweetCookie; check Chrome profile %q and OS Keychain access", profile)
	}
	s := session{Profile: profile, ImportedAt: time.Now().UTC()}
	for _, c := range result.Cookies {
		cookie := savedCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly}
		if cookie.Path == "" {
			cookie.Path = "/"
		}
		if validCookie(cookie) && (cookie.Expires == nil || cookie.Expires.After(s.ImportedAt)) {
			s.Cookies = append(s.Cookies, cookie)
		}
	}
	status := statusFor(path, s)
	if !status.SessionAvailable {
		return AuthStatus{}, fmt.Errorf("no usable Ozon Tech session in Chrome profile %q; open %s in Chrome, then auth import", profile, Origin)
	}
	if err := ctx.Err(); err != nil {
		return AuthStatus{}, err
	}
	if err := SavePrivateJSON(path, s); err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}

func statusFor(path string, s session) AuthStatus {
	status := AuthStatus{Path: path, Imported: !s.ImportedAt.IsZero(), Profile: s.Profile, CookieNames: []string{}}
	if !status.Imported {
		return status
	}
	status.ImportedAt = &s.ImportedAt
	for _, c := range s.Cookies {
		if c.Expires != nil && !c.Expires.After(time.Now()) {
			continue
		}
		status.CookieNames = append(status.CookieNames, c.Name)
		if c.Value != "" {
			status.SessionAvailable = true
		}
	}
	slices.Sort(status.CookieNames)
	status.CookieNames = slices.Compact(status.CookieNames)
	return status
}

func loadSession(path string) (session, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return session{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128<<10 {
		return session{}, fmt.Errorf("session file %q must be an owner-only regular file no larger than 128 KiB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return session{}, fmt.Errorf("read session file %q: %w", path, err)
	}
	var s session
	if json.Unmarshal(b, &s) != nil || s.ImportedAt.IsZero() || s.Profile == "" {
		return session{}, fmt.Errorf("invalid session file %q; run auth import again", path)
	}
	for _, c := range s.Cookies {
		if !validCookie(c) {
			return session{}, fmt.Errorf("unsupported cookie in session file %q; run auth import again", path)
		}
	}
	return s, nil
}

func SessionStatus(path string) (AuthStatus, error) {
	s, err := loadSession(path)
	if errors.Is(err, os.ErrNotExist) {
		return statusFor(path, session{}), nil
	}
	if err != nil {
		return AuthStatus{}, err
	}
	return statusFor(path, s), nil
}

func SessionClient(path string) (*http.Client, error) {
	s, err := loadSession(path)
	if err != nil {
		return nil, fmt.Errorf("load saved Ozon Tech session %q: %w; run auth import", path, err)
	}
	if !statusFor(path, s).SessionAvailable {
		return nil, fmt.Errorf("saved session %q is missing or expired; run auth import", path)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	for _, c := range s.Cookies {
		cookie := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.Expires != nil {
			cookie.Expires = *c.Expires
		}
		jar.SetCookies(&url.URL{Scheme: "https", Host: strings.TrimPrefix(c.Domain, "."), Path: "/"}, []*http.Cookie{cookie})
	}
	return &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		Transport:     http.DefaultTransport.(*http.Transport).Clone(),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

func SavePrivateJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("destination directory %q must be a regular directory", dir)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("destination %q must be a regular file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".ozon-tech-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(b, '\n'))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
