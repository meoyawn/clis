package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestProtectionStopsAfterOneRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{"forbidden", 403, "Forbidden"}, {"qrator", 200, `<script src="/__qrator/qauth.js"></script>`}, {"servicepipe", 200, `<script src="https://servicepipe.tech/loader.js"></script>`}} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{http: srv.Client(), origin: srv.URL, session: Session{UserAgent: BrowserUserAgent}}
			_, err := c.fetch(context.Background(), http.MethodGet, srv.URL+"/catalogue/", "", nil, nil)
			var blocked *BlockedError
			if !errors.As(err, &blocked) || blocked.URL != srv.URL+"/catalogue/" || requests != 1 || c.requests != 1 {
				t.Fatalf("blocked=%v requests=%d count=%d", err, requests, c.requests)
			}
		})
	}
}
func TestSessionStaysPrivateAndRejectsAccountCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "session.json")
	u := strings.TrimPrefix(Origin, "https://")
	u = strings.TrimPrefix(u, "kazan.")
	s := Session{UserAgent: BrowserUserAgent, ImportedAt: time.Now(), Profile: "Default", Cookies: []SavedCookie{{Name: cookieNames[0], Value: "synthetic-secret", Domain: u, Path: "/"}}}
	if err := SaveSession(path, s); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions=%v", info.Mode())
	}
	restored, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Cookies) != 1 || restored.Cookies[0].Value != "synthetic-secret" {
		t.Fatal("session did not round-trip")
	}
	s.Cookies[0].Name = "account-token"
	if err = SaveSession(path, s); err == nil {
		t.Fatal("account cookie was saved")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSession(path); err == nil {
		t.Fatal("world-readable session was accepted")
	}
}
