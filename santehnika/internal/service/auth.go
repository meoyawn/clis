package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/steipete/sweetcookie"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const BrowserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"

type Session struct {
	UserAgent  string        `json:"user_agent"`
	ImportedAt time.Time     `json:"imported_at"`
	Profile    string        `json:"profile"`
	PageURL    string        `json:"page_url,omitempty"`
	Cookies    []SavedCookie `json:"cookies"`
}
type SavedCookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Domain   string     `json:"domain"`
	Path     string     `json:"path"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure"`
	HTTPOnly bool       `json:"http_only"`
}
type AuthStatus struct {
	Path         string   `json:"path"`
	Imported     bool     `json:"imported"`
	Profile      string   `json:"profile,omitempty"`
	CookieNames  []string `json:"cookie_names"`
	HTTPRequests int      `json:"http_requests"`
}

func SessionPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, Name, "session.json"), nil
}
func validCookie(c SavedCookie) bool {
	u, _ := url.Parse(Origin)
	root := strings.TrimPrefix(u.Hostname(), "kazan.")
	return (strings.TrimPrefix(c.Domain, ".") == root || strings.TrimPrefix(c.Domain, ".") == u.Hostname()) && c.Path == "/" && c.Value != "" && (&http.Cookie{Name: c.Name, Value: c.Value}).Valid() == nil
}
func LoadSession(path string) (Session, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128<<10 {
		return Session{}, fmt.Errorf("session file must be an owner-only regular file smaller than 128 KiB")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err = json.Unmarshal(b, &s); err != nil {
		return Session{}, fmt.Errorf("decode saved session: %w", err)
	}
	if s.ImportedAt.IsZero() || s.UserAgent == "" || strings.ContainsAny(s.UserAgent, "\r\n") {
		return Session{}, fmt.Errorf("invalid saved session; run auth import again")
	}
	for _, c := range s.Cookies {
		if !validCookie(c) {
			return Session{}, fmt.Errorf("saved session contains an unsupported cookie; run auth import again")
		}
	}
	return s, nil
}
func SaveSession(path string, s Session) error {
	for _, c := range s.Cookies {
		if !validCookie(c) {
			return fmt.Errorf("refusing to save an unsupported cookie")
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("session directory must not be a symlink")
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(b)
	closeErr := f.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func ImportSession(ctx context.Context, path, profile, ua string) (*AuthStatus, error) {
	if ua == "" {
		ua = BrowserUserAgent
	}
	if strings.ContainsAny(ua, "\r\n") {
		return nil, fmt.Errorf("user-agent must not contain newlines")
	}
	opts := sweetcookie.Options{URL: Origin + "/", Browsers: []sweetcookie.Browser{sweetcookie.BrowserChrome}, Mode: sweetcookie.ModeFirst, Timeout: 30 * time.Second}
	if profile != "" {
		opts.Profiles = map[sweetcookie.Browser]string{sweetcookie.BrowserChrome: profile}
	}
	r, err := sweetcookie.Get(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("read Chrome cookies using sweetcookie: check profile and OS Keychain access")
	}
	s := Session{UserAgent: ua, ImportedAt: time.Now().UTC(), Profile: profile}
	for _, c := range r.Cookies {
		saved := SavedCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly}
		if saved.Path == "" {
			saved.Path = "/"
		}
		if validCookie(saved) && (saved.Expires == nil || saved.Expires.After(time.Now())) {
			s.Cookies = append(s.Cookies, saved)
		}
	}
	hasProtection := false
	for _, cookie := range s.Cookies {
		if strings.HasPrefix(cookie.Name, "qrator_") || cookie.Name == "spsc" || cookie.Name == "spjs" {
			hasProtection = true
		}
	}
	if !hasProtection {
		return nil, fmt.Errorf("no usable catalog cookies found; open %s in Chrome, complete any check manually, then import the same profile", Origin)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = SaveSession(path, s); err != nil {
		return nil, err
	}
	return SessionStatus(path, s), nil
}
func SessionStatus(path string, s Session) *AuthStatus {
	r := &AuthStatus{Path: path, Imported: !s.ImportedAt.IsZero(), Profile: s.Profile, CookieNames: []string{}}
	for _, c := range s.Cookies {
		if c.Expires == nil || c.Expires.After(time.Now()) {
			r.CookieNames = append(r.CookieNames, c.Name)
		}
	}
	slices.Sort(r.CookieNames)
	r.CookieNames = slices.Compact(r.CookieNames)
	return r
}
