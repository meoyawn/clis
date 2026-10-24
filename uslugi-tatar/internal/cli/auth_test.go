package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivateSessionLifecycle(t *testing.T) {
	t.Setenv("USLUGI_TATAR_CONFIG_DIR", filepath.Join(t.TempDir(), "private"))
	path, err := authFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(path); err == nil || !strings.Contains(err.Error(), "auth import") {
		t.Fatalf("missing session should explain how to import: %v", err)
	}
	session := savedSession{Version: 1, Browser: "chrome", ImportedAt: time.Now(), Cookies: []*http.Cookie{
		{Name: "session", Value: "synthetic-private-value", Domain: "uslugi.tatarstan.ru", Path: "/", Secure: true, HttpOnly: true},
	}}
	if err := saveSession(path, session); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		path string
		mode os.FileMode
	}{{path, 0600}, {filepath.Dir(path), 0700}} {
		info, err := os.Stat(entry.path)
		if err != nil || info.Mode().Perm() != entry.mode {
			t.Fatalf("private session permissions: info=%v, error=%v", info, err)
		}
	}
	saved, err := loadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sessionClient(saved)
	if err != nil {
		t.Fatal(err)
	}
	origin, _ := url.Parse(portalURL)
	cookies := client.http.Jar.Cookies(origin)
	if len(cookies) != 1 || cookies[0].Name != "session" || cookies[0].Value != "synthetic-private-value" {
		t.Fatal("persisted session was not usable by the HTTP client")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("insecure session should be rejected: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"synthetic-private-value":"do not echo JSON decoder errors"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSession(path); err == nil || strings.Contains(err.Error(), "synthetic-private-value") {
		t.Fatalf("malformed cache errors must not expose its contents: %v", err)
	}
	for _, test := range []struct {
		name, domain string
		expires      time.Time
	}{
		{name: "expired", domain: "uslugi.tatarstan.ru", expires: time.Now().Add(-time.Hour)},
		{name: "other origin", domain: "example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session.Cookies[0].Domain, session.Cookies[0].Expires = test.domain, test.expires
			if _, err := sessionClient(session); err == nil || strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatalf("unusable session must be rejected without cookie values: %v", err)
			}
		})
	}
}

func TestLiveAuthenticationCheck(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		wantError  string
	}{
		{name: "signed in with hidden login popup", status: 200, body: `<div hidden><button id="enter_button">Login</button></div><a href="/user/logout">Logout</a>`},
		{name: "signed out", status: 200, body: `<button id="enter_button">Login</button>`, wantError: "not authenticated"},
		{name: "foreign logout link", status: 200, body: `<a href="https://example.com/user/logout">Logout</a>`, wantError: "not authenticated"},
		{name: "portal unavailable", status: 503, wantError: "HTTP 503"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/" || req.Method != http.MethodGet {
					t.Error("authentication check must only GET the homepage")
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			client, err := newPortalClient(server.URL, &http.Client{Jar: jar, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			err = client.checkAuth(context.Background())
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("authentication result: %v; want %q", err, test.wantError)
			}
			if test.status == 503 && errors.Is(err, errNotAuthenticated) {
				t.Fatal("network/portal failure must not claim authentication expired")
			}
		})
	}
}
