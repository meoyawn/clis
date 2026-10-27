package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/sweetcookie"
)

type Session struct {
	AuthToken  string     `json:"auth_token"`
	CSRF       string     `json:"ct0"`
	TWID       string     `json:"twid"`
	Profile    string     `json:"profile"`
	ImportedAt time.Time  `json:"imported_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func (s Session) Validate() error {
	for _, c := range []*http.Cookie{{Name: "auth_token", Value: s.AuthToken}, {Name: "ct0", Value: s.CSRF}, {Name: "twid", Value: s.TWID}} {
		if c.Value == "" || c.Valid() != nil {
			return fmt.Errorf("missing or invalid %s cookie; sign in at x.com and run twitter auth import", c.Name)
		}
	}
	if s.ExpiresAt != nil && !s.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("saved session expired at %s; sign in at x.com and run twitter auth import", s.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

func (s Session) UserID() string {
	value, _ := url.QueryUnescape(strings.Trim(s.TWID, `"`))
	return strings.TrimPrefix(value, "u=")
}

func ImportSession(ctx context.Context, profile string) (Session, error) {
	result, err := sweetcookie.Get(ctx, sweetcookie.Options{URL: "https://x.com/", Names: []string{"auth_token", "ct0", "twid"}, Browsers: []sweetcookie.Browser{sweetcookie.BrowserChrome}, Profiles: map[sweetcookie.Browser]string{sweetcookie.BrowserChrome: profile}, Mode: sweetcookie.ModeMerge, Timeout: 30 * time.Second})
	if err != nil {
		return Session{}, fmt.Errorf("SweetCookie could not read the selected Chrome profile; check that x.com is signed in and Keychain access is available")
	}
	type source struct {
		store     sweetcookie.Source
		container int
	}
	groups := make(map[source]*Session)
	var order []source
	for _, cookie := range result.Cookies {
		key := source{cookie.Source, cookie.Container.ID}
		s := groups[key]
		if s == nil {
			s = &Session{Profile: cookie.Source.Profile, ImportedAt: time.Now().UTC()}
			groups[key] = s
			order = append(order, key)
		}
		switch cookie.Name {
		case "auth_token":
			s.AuthToken = cookie.Value
		case "ct0":
			s.CSRF = cookie.Value
		case "twid":
			s.TWID = cookie.Value
		}
		if cookie.Expires != nil && (s.ExpiresAt == nil || cookie.Expires.Before(*s.ExpiresAt)) {
			s.ExpiresAt = cookie.Expires
		}
	}
	var found []Session
	for _, key := range order {
		if groups[key].Validate() == nil {
			found = append(found, *groups[key])
		}
	}
	if len(found) != 1 {
		return Session{}, fmt.Errorf("found %d complete X cookie sets; select one Chrome profile with --profile (auth_token, ct0 and twid must share a source)", len(found))
	}
	return found[0], nil
}

func LoadSession(dir string) (Session, error) {
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return Session{}, fmt.Errorf("read saved session: run twitter auth import (%w)", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("invalid saved session; run twitter auth import")
	}
	return s, s.Validate()
}

func SaveSession(dir string, s Session) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("protect session directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(s); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "session.json"))
}
