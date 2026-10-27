package service

import (
	"context"
	"errors"
	"github.com/meoyawn/clis/santehnika/internal/domain"
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
			c := localCatalogClient(t, srv.URL)
			_, err := c.fetch(context.Background(), http.MethodGet, srv.URL+"/catalogue/", "", nil, nil)
			var blocked *BlockedError
			if !errors.As(err, &blocked) || blocked.URL != srv.URL+"/catalogue/" || requests != 1 || c.requests != 1 {
				t.Fatalf("blocked=%v requests=%d count=%d", err, requests, c.requests)
			}
		})
	}
}
func TestSessionStaysPrivateAndPreservesBrowserCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "session.json")
	u := strings.TrimPrefix(Origin, "https://")
	u = strings.TrimPrefix(u, "kazan.")
	s := Session{UserAgent: BrowserUserAgent, ImportedAt: time.Now(), Profile: "Default", Cookies: []SavedCookie{{Name: "Fuuid", Value: "synthetic-secret", Domain: u, Path: "/"}}}
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
	s.Cookies[0].Domain = "other.example"
	if err = SaveSession(path, s); err == nil {
		t.Fatal("cookie from another site was saved")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSession(path); err == nil {
		t.Fatal("world-readable session was accepted")
	}
}

func localCatalogClient(t *testing.T, origin string) *Client {
	t.Helper()
	cookies := []SavedCookie{{Name: "_lcid", Value: "1283", Domain: "santehnika-online.ru", Path: "/"}}
	for _, name := range []string{"_laddr", "_lcname", "_llocality", "_lregname"} {
		cookies = append(cookies, SavedCookie{Name: name, Value: "synthetic-city", Domain: "santehnika-online.ru", Path: "/"})
	}
	c, err := NewClient(Session{UserAgent: BrowserUserAgent, Cookies: cookies})
	if err != nil {
		t.Fatal(err)
	}
	c.origin = origin
	return c
}

func TestCatalogRejectsRegionFallback(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.SetCookie(w, &http.Cookie{Name: "_lcid", Value: "2097", Domain: ".santehnika-online.ru", Path: "/"})
		_, _ = w.Write(fixture(t, "list.json"))
	}))
	defer srv.Close()
	c := localCatalogClient(t, srv.URL)
	result, err := c.Section(context.Background(), "5316", Options{Page: 1, Sort: domain.Popular()})
	if err == nil || result != nil || !strings.Contains(err.Error(), "Kazan") || requests != 1 {
		t.Fatalf("region fallback must not report Kazan stock: result=%+v err=%v requests=%d", result, err, requests)
	}
}
