package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sweetcookie"
)

func cookieReader(t *testing.T) CookieReader {
	t.Helper()
	return func(_ context.Context, o sweetcookie.Options) (sweetcookie.Result, error) {
		t.Helper()
		if o.URL != WebOrigin+"/" || len(o.Browsers) != 1 || string(o.Browsers[0]) != "chrome" || o.Profiles[sweetcookie.Browser("chrome")] != "Profile 1" {
			t.Fatalf("wrong cookie import options: %+v", o)
		}
		expired := time.Now().Add(-time.Hour)
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{
			{Name: "session", Value: "new-secret-cookie", Domain: ".ozon.ru", Path: "/", Secure: true},
			{Name: "expired", Value: "not-usable", Domain: ".ozon.ru", Path: "/", Expires: &expired},
			{Name: "unrelated", Value: "do-not-import", Domain: ".example.invalid", Path: "/"},
			{Name: "wrong-path", Value: "do-not-import", Domain: ".ozon.ru", Path: "/cart"},
		}}, nil
	}
}

func TestImportCopiesBrowserSessionWithoutHTTP(t *testing.T) {
	t.Parallel()
	store := AuthStore{Path: filepath.Join(t.TempDir(), "ozon", "auth.json")}
	c, err := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("import contacted Ozon instead of copying the browser session")
		return nil, nil
	})}, store)
	if err != nil {
		t.Fatal(err)
	}
	read := func(ctx context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		r, err := cookieReader(t)(ctx, opts)
		r.Cookies = append(r.Cookies,
			sweetcookie.Cookie{Name: "__Secure-access-token", Value: "browser-access-token", Domain: ".ozon.ru", Path: "/", Secure: true, HTTPOnly: true},
			sweetcookie.Cookie{Name: "__Secure-refresh-token", Value: "browser-renewal-token", Domain: ".ozon.ru", Path: "/", Secure: true},
			sweetcookie.Cookie{Name: "ozonIdAuthResponseToken", Value: "browser-login-exchange", Domain: ".ozon.ru", Path: "/", Secure: true},
		)
		return r, err
	}
	status, err := c.ImportAuth(t.Context(), ChromeBrowser(), "Profile 1", read)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Imported || status.HTTPRequests != 0 || status.LoginVerified || status.IsAuthenticated != nil || status.Account != nil || status.CookieCount != 2 {
		t.Fatalf("import claimed network verification or imported renewal credentials: %+v", status)
	}
	record, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Cookies) != 2 || record.Cookies[0].Name != "session" || record.Cookies[0].Value != "new-secret-cookie" || record.Cookies[1].Name != "__Secure-access-token" || record.Cookies[1].Value != "browser-access-token" || record.Cookies[1].Domain != ".ozon.ru" || record.Cookies[1].Path != "/" || !record.Cookies[1].HTTPOnly {
		t.Fatal("import did not preserve the browser session credentials")
	}
	file, err := os.Stat(store.Path)
	if err != nil || file.Mode().Perm() != 0600 {
		t.Fatal("auth file must be 0600", err)
	}
	dir, err := os.Stat(filepath.Dir(store.Path))
	if err != nil || dir.Mode().Perm() != 0700 {
		t.Fatal("auth directory must be 0700", err)
	}
	b, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"new-secret-cookie", "browser-access-token", "browser-renewal-token", "browser-login-exchange", "do-not-import"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("secret in auth diagnostics")
		}
	}
}

func TestFailedImportPreservesSession(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"reader", "empty", "mixed-profiles"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			store := testStore(t)
			before, err := os.ReadFile(store.Path)
			if err != nil {
				t.Fatal(err)
			}
			c, err := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("import issued a network request")
				return nil, nil
			})}, store)
			if err != nil {
				t.Fatal(err)
			}
			read := func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
				switch mode {
				case "reader":
					return sweetcookie.Result{}, errors.New("browser-secret-token")
				case "empty":
					return sweetcookie.Result{}, nil
				default:
					return sweetcookie.Result{Cookies: []sweetcookie.Cookie{
						{Name: "session", Value: "one", Domain: ".ozon.ru", Path: "/", Source: sweetcookie.Source{StorePath: "/one/Cookies"}},
						{Name: "session", Value: "two", Domain: ".ozon.ru", Path: "/", Source: sweetcookie.Source{StorePath: "/two/Cookies"}},
					}}, nil
				}
			}
			if _, err := c.ImportAuth(t.Context(), ChromeBrowser(), "Profile 1", read); err == nil || strings.Contains(err.Error(), "browser-secret-token") {
				t.Fatal("failed import accepted or leaked browser diagnostic", err)
			}
			after, err := os.ReadFile(store.Path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed import overwrote session", err)
			}
		})
	}
}

func TestExpiredAndInsecureStores(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	r, e := store.load()
	if e != nil {
		t.Fatal(e)
	}
	old := time.Now().Add(-time.Hour)
	r.Cookies[0].Expires = &old
	if e = store.save(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	c, e := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("expired cookie caused request"); return nil, nil })}, store)
	if e != nil {
		t.Fatal(e)
	}
	s, e := c.AuthStatus(t.Context(), RequestOptions{})
	if e == nil || s.HTTPRequests != 0 || s.ExpiredCookieCount != 1 {
		t.Fatal("expired cookies accepted")
	}
	if e = os.Chmod(store.Path, 0644); e != nil { // #nosec G302 -- Deliberately insecure permissions exercise rejection.
		t.Fatal(e)
	}
	if _, e = store.load(); e == nil {
		t.Fatal("world-readable cookies accepted")
	}
}

func TestCanceledAuthDoesNotIssueRequest(t *testing.T) {
	t.Parallel()
	c := testClient(t, func(*http.Request) (*http.Response, error) { t.Fatal("request after cancellation"); return nil, nil })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, err := c.AuthStatus(ctx, RequestOptions{})
	if !errors.Is(err, context.Canceled) || s.HTTPRequests != 0 {
		t.Fatalf("status=%+v error=%v", s, err)
	}
}

func TestAuthStatusReportsLoginFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		body     string
		verified bool
		status   int
	}{
		{"anonymous", `{"widgetStates":{"profileMenuAnonymous-123-default-1":"{\"iconHeader\":{\"subtitle\":{\"text\":\"Войти\"}}}"}}`, true, http.StatusOK},
		{"conflicting", `{"widgetStates":{"profileMenuAnonymous-123-default-1":"{}"},"userInfo":{"user":{"isLoggedIn":true}}}`, false, http.StatusOK},
		{"malformed", `{"widgetStates":{"profileMenuAnonymous-123-default-1":"broken"}}`, false, http.StatusOK},
		{"unknown", `{"widgetStates":{"profileLogo-123-default-1":"{}"}}`, false, http.StatusOK},
		{"http", "browser-secret-token", false, http.StatusForbidden},
		{"challenge", `{"challengeURL":"https://www.ozon.ru/challenge.html?token=browser-secret-token"}`, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := testClient(t, func(*http.Request) (*http.Response, error) {
				return response([]byte(tc.body), tc.status), nil
			})
			s, err := c.AuthStatus(t.Context(), RequestOptions{})
			if err == nil || s.LoginVerified != tc.verified || s.Account != nil || s.HTTPRequests != 1 {
				t.Fatalf("status=%+v error=%v", s, err)
			}
			if tc.verified && (s.IsAuthenticated == nil || *s.IsAuthenticated || !strings.Contains(err.Error(), "signed-out page")) {
				t.Fatalf("anonymous page was not reported as signed out: %+v %v", s, err)
			}
			if strings.Contains(err.Error(), "browser-secret-token") {
				t.Fatal("status leaked the failure response body")
			}
			if !tc.verified && s.IsAuthenticated != nil {
				t.Fatal("unknown or invalid login state was reported as verified")
			}
		})
	}
}

func TestAuthStatusChecksSavedCookiesWithoutSaving(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	record, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	// Older versions stored renewal credentials. They must never leave the CLI.
	record.Cookies = append(record.Cookies,
		storedCookie{Name: "__Secure-refresh-token", Value: "legacy-refresh", Domain: ".ozon.ru", Path: "/", Secure: true},
		storedCookie{Name: "ozonIdAuthResponseToken", Value: "legacy-exchange", Domain: ".ozon.ru", Path: "/", Secure: true},
	)
	if err := store.save(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "secret-cookie-value" {
			t.Fatal("status did not check the saved CLI cookies")
		}
		for _, name := range []string{"__Secure-refresh-token", "ozonIdAuthResponseToken"} {
			if _, err := r.Cookie(name); err == nil {
				t.Fatal("status sent browser renewal credentials")
			}
		}
		resp := response(fixture(t, "auth"), http.StatusOK)
		resp.Header.Add("Set-Cookie", "session=rotated-cookie; Domain=.ozon.ru; Path=/; Secure; HttpOnly")
		return resp, nil
	})}, store)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.AuthStatus(t.Context(), RequestOptions{})
	if err != nil || !s.LoginVerified || s.HTTPRequests != 1 {
		t.Fatalf("status=%+v error=%v", s, err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("status changed the saved cookies or import timestamp")
	}
}
