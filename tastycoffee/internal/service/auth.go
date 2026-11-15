package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

const Origin = "https://shop.tastycoffee.ru"

type SavedCookie struct {
	Name    string     `json:"name"`
	Value   string     `json:"value"`
	Domain  string     `json:"domain"`
	Path    string     `json:"path"`
	Expires *time.Time `json:"expires,omitempty"`
}

type Session struct {
	Profile    string      `json:"profile"`
	ImportedAt time.Time   `json:"imported_at"`
	Cookie     SavedCookie `json:"cookie"`
}

type AuthStatus struct {
	Path             string     `json:"path"`
	Imported         bool       `json:"imported"`
	SessionAvailable bool       `json:"session_available"`
	Profile          string     `json:"profile,omitempty"`
	ImportedAt       *time.Time `json:"imported_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	AccountID        *int64     `json:"account_id,omitempty"`
}

func SessionPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(dir, "tastycoffee", "session.json"), nil
}

func validCookie(c SavedCookie) bool {
	host := strings.TrimPrefix(c.Domain, ".")
	return c.Name == "auth.token" && (host == "shop.tastycoffee.ru" || host == "tastycoffee.ru") &&
		c.Path == "/" && c.Value != "" &&
		(&http.Cookie{Name: c.Name, Value: c.Value}).Valid() == nil
}

func (s Session) Token() (string, error) {
	if !validCookie(s.Cookie) {
		return "", fmt.Errorf("saved session has an invalid auth.token cookie; run auth import")
	}
	if s.Cookie.Expires != nil && !s.Cookie.Expires.After(time.Now()) {
		return "", fmt.Errorf("saved session has expired; sign in in Chrome and run auth import")
	}
	token, err := url.PathUnescape(s.Cookie.Value)
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\t \x00") {
		return "", fmt.Errorf("saved session has an invalid authentication token; run auth import")
	}
	return token, nil
}

// ImportSession is the only operation that reads Chrome or the OS keychain.
func ImportSession(ctx context.Context, path, profile string) (AuthStatus, error) {
	if strings.TrimSpace(profile) == "" {
		return AuthStatus{}, fmt.Errorf("invalid Chrome profile %q; use --browser-profile", profile)
	}
	result, err := sweetcookie.Get(ctx, sweetcookie.Options{
		URL: Origin + "/", Names: []string{"auth.token"},
		Browsers: []sweetcookie.Browser{sweetcookie.BrowserChrome},
		Profiles: map[sweetcookie.Browser]string{sweetcookie.BrowserChrome: profile},
		Mode:     sweetcookie.ModeFirst, Timeout: 30 * time.Second,
	})
	if err != nil {
		return AuthStatus{}, fmt.Errorf("read Chrome profile %q using sweetcookie; check profile and OS Keychain access", profile)
	}
	s := Session{Profile: profile, ImportedAt: time.Now().UTC()}
	for _, c := range result.Cookies {
		cookie := SavedCookie{Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires}
		if cookie.Path == "" {
			cookie.Path = "/"
		}
		candidate := Session{Cookie: cookie}
		if _, err := candidate.Token(); err != nil {
			continue
		}
		if s.Cookie.Value != "" && s.Cookie.Value != cookie.Value {
			return AuthStatus{}, fmt.Errorf("conflicting auth.token cookies in Chrome profile %q; sign in again in that profile", profile)
		}
		s.Cookie = cookie
	}
	if s.Cookie.Value == "" {
		return AuthStatus{}, fmt.Errorf("no usable Tasty Coffee session in Chrome profile %q; sign in to %s and run auth import", profile, Origin)
	}
	if err := ctx.Err(); err != nil {
		return AuthStatus{}, err
	}
	if err := SaveSession(path, s); err != nil {
		return AuthStatus{}, err
	}
	return Status(path, s), nil
}

func Status(path string, s Session) AuthStatus {
	status := AuthStatus{Path: path, Imported: !s.ImportedAt.IsZero()}
	if !status.Imported {
		return status
	}
	status.Profile, status.ImportedAt, status.ExpiresAt = s.Profile, &s.ImportedAt, s.Cookie.Expires
	_, err := s.Token()
	status.SessionAvailable = err == nil
	return status
}

// LoadSession returns an empty session when no import has been saved yet.
func LoadSession(path string) (Session, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, nil
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128<<10 {
		return Session{}, fmt.Errorf("session file %q must be an owner-only regular file no larger than 128 KiB", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, fmt.Errorf("read session file %q: %w", path, err)
	}
	var s Session
	if json.Unmarshal(b, &s) != nil || s.ImportedAt.IsZero() || s.Profile == "" || !validCookie(s.Cookie) {
		return Session{}, fmt.Errorf("invalid session file %q; run auth import again", path)
	}
	return s, nil
}

func SaveSession(path string, s Session) error {
	if s.ImportedAt.IsZero() || s.Profile == "" {
		return fmt.Errorf("session must have a profile and import time")
	}
	if _, err := s.Token(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("session directory %q must be a regular directory", dir)
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("session destination %q must be a regular file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
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
	_, writeErr := f.Write(append(b, '\n'))
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
