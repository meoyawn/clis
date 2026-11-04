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

const Origin = "https://app.ahrefs.com"

var cookieNames = []string{"BSSESSID", "x-consistency", "x-consistency-b", "cf_clearance", "__cf_bm", "__cflb"}

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
	SessionAvailable bool       `json:"session_available"`
	Profile          string     `json:"profile,omitempty"`
	ImportedAt       *time.Time `json:"imported_at,omitempty"`
	CookieNames      []string   `json:"cookie_names"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func SessionPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate Ahrefs config directory: %w", err)
	}
	return filepath.Join(dir, "ahrefs", "session.json"), nil
}

// Only this explicit operation accesses Chrome or SweetCookie.
func ImportSession(ctx context.Context, path, profile, cookieFile string) (AuthStatus, error) {
	if profile == "" {
		return AuthStatus{}, fmt.Errorf("invalid Chrome profile %q; select a profile with --browser-profile", profile)
	}
	opts := sweetcookie.Options{
		URL: Origin + "/v4/saProjects", Names: slices.Clone(cookieNames),
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
		return AuthStatus{}, fmt.Errorf("read Ahrefs cookies through SweetCookie; check Chrome profile %q and OS Keychain access", profile)
	}
	s := session{Profile: profile, ImportedAt: time.Now().UTC()}
	for _, c := range result.Cookies {
		saved := savedCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly}
		if saved.Path == "" {
			saved.Path = "/"
		}
		if validCookie(saved) && (saved.Expires == nil || saved.Expires.After(s.ImportedAt)) {
			s.Cookies = append(s.Cookies, saved)
		}
	}
	status := statusFor(path, s)
	if !status.SessionAvailable {
		return AuthStatus{}, fmt.Errorf("no usable Ahrefs session in Chrome profile %q; sign in to %s in that profile", profile, Origin)
	}
	if err := ctx.Err(); err != nil {
		return AuthStatus{}, err
	}
	if err := saveSession(path, s); err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}

func validCookie(c savedCookie) bool {
	host := strings.TrimPrefix(c.Domain, ".")
	return (host == "ahrefs.com" || host == "app.ahrefs.com") && slices.Contains(cookieNames, c.Name) &&
		c.Value != "" && strings.HasPrefix(c.Path, "/") &&
		(&http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path}).Valid() == nil
}

func sessionJar(s session) (http.CookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create Ahrefs cookie jar: %w", err)
	}
	for _, c := range s.Cookies {
		cookie := &http.Cookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Secure: c.Secure, HttpOnly: c.HTTPOnly}
		if c.Expires != nil {
			cookie.Expires = *c.Expires
		}
		jar.SetCookies(&url.URL{Scheme: "https", Host: strings.TrimPrefix(c.Domain, "."), Path: c.Path}, []*http.Cookie{cookie})
	}
	return jar, nil
}

func statusFor(path string, s session) AuthStatus {
	status := AuthStatus{Path: path, Imported: !s.ImportedAt.IsZero(), Profile: s.Profile, CookieNames: []string{}}
	if !status.Imported {
		return status
	}
	status.ImportedAt = &s.ImportedAt
	now := time.Now()
	for _, c := range s.Cookies {
		if c.Expires != nil && !c.Expires.After(now) {
			continue
		}
		// A session scoped to one endpoint is insufficient for the workflow.
		if c.Path != "/" && c.Path != "/v4" && c.Path != "/v4/" {
			continue
		}
		status.CookieNames = append(status.CookieNames, c.Name)
		if c.Name == "BSSESSID" {
			status.SessionAvailable = true
			status.ExpiresAt = c.Expires
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
		return session{}, fmt.Errorf("saved Ahrefs session file %q must be an owner-only regular file no larger than 128 KiB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return session{}, fmt.Errorf("read Ahrefs session file %q: %w", path, err)
	}
	var s session
	if err := json.Unmarshal(b, &s); err != nil {
		return session{}, fmt.Errorf("invalid Ahrefs session file %q; run auth import again", path)
	}
	if s.ImportedAt.IsZero() || s.Profile == "" {
		return session{}, fmt.Errorf("incomplete Ahrefs session file %q; run auth import again", path)
	}
	for _, c := range s.Cookies {
		if !validCookie(c) {
			return session{}, fmt.Errorf("saved Ahrefs session file %q contains an unsupported cookie; run auth import again", path)
		}
	}
	return s, nil
}

func saveSession(path string, s session) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create Ahrefs session directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("session directory %q must not be a symlink", dir)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("session destination %q must be a regular file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode Ahrefs session: %w", err)
	}
	file, err := os.CreateTemp(dir, ".ahrefs-session-*")
	if err != nil {
		return fmt.Errorf("create Ahrefs session file: %w", err)
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(b)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write Ahrefs session file: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("save Ahrefs session file %q: %w", path, err)
	}
	return nil
}

// SessionStatus is local and never opens Chrome, Keychain, or a network connection.
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
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no saved Ahrefs session at %q; run auth import first", path)
	}
	if err != nil {
		return nil, err
	}
	if !statusFor(path, s).SessionAvailable {
		return nil, fmt.Errorf("saved Ahrefs session at %q is missing or expired; run auth import again", path)
	}
	jar, err := sessionJar(s)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Jar: jar, Timeout: 30 * time.Second,
		Transport:     http.DefaultTransport.(*http.Transport).Clone(),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}
