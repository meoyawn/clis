package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpiredSnapshotResyncsSelectedChromeAccount(t *testing.T) {
	// SweetCookie's documented override prevents any real Keychain access.
	t.Setenv("GOOKIE_CHROME_SAFE_STORAGE_PASSWORD", "synthetic-test-key")
	for _, tc := range []struct {
		name, browserUser string
		browserExpired    bool
		wantError         string
	}{
		{name: "renewed browser session", browserUser: "same-user"},
		{name: "browser also expired", browserUser: "same-user", browserExpired: true, wantError: "expired"},
		{name: "browser account changed", browserUser: "different-user", wantError: "account changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			profile := filepath.Join(dir, "Cookies")
			browserExpiry := time.Now().Add(time.Hour)
			if tc.browserExpired {
				browserExpiry = time.Now().Add(-time.Hour)
			}
			browserCookies := syntheticAuthCookies(tc.browserUser, browserExpiry)
			db, err := sql.Open("sqlite", profile)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`CREATE TABLE cookies(host_key TEXT, name TEXT, path TEXT, value TEXT, encrypted_value BLOB, expires_utc INTEGER, is_secure INTEGER, is_httponly INTEGER, samesite INTEGER)`); err != nil {
				t.Fatal(err)
			}
			for _, cookie := range browserCookies {
				if _, err = db.Exec(`INSERT INTO cookies VALUES(?, ?, '/', ?, X'', 0, 1, 0, 0)`, ".levels.fyi", cookie.Name, cookie.Value); err != nil {
					t.Fatal(err)
				}
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "session.json")
			old := &Session{Profile: profile, Headers: http.Header{"User-Agent": {"synthetic-browser"}}, Cookies: syntheticAuthCookies("same-user", time.Now().Add(-time.Hour))}
			if err = SaveSession(path, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := LoadAuthorizedSession(context.Background(), path)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q failure, got %v", tc.wantError, err)
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(before) != string(after) {
					t.Fatal("failed resync replaced the saved session")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.Headers.Get("Authorization") != "Bearer "+browserCookies[1].Value || s.Profile != profile || s.Headers.Get("User-Agent") != "synthetic-browser" {
				t.Fatal("resync did not preserve the selected profile/headers and use its current token")
			}
			persisted, err := LoadSession(path)
			if err != nil || persisted.Headers.Get("Authorization") != s.Headers.Get("Authorization") {
				t.Fatal("renewed credentials were not persisted for the next invocation")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("saved credentials are not private")
			}
		})
	}
}

func syntheticAuthCookies(user string, expiry time.Time) []*http.Cookie {
	const prefix = "CognitoIdentityServiceProvider.76of0ich18hd8uehju7fniv2u1."
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expiry.Unix())))
	return []*http.Cookie{
		{Name: prefix + "LastAuthUser", Value: user, Domain: "levels.fyi", Path: "/", Secure: true},
		{Name: prefix + user + ".idToken", Value: "synthetic." + payload + ".signature", Domain: "levels.fyi", Path: "/", Secure: true},
	}
}
