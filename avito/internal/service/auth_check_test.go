package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sweetcookie"
)

func TestAuthProfileRequiresAnExplicitCurrentAccount(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/web_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	account, err := parseAuthProfile(fixture)
	if err != nil || account == nil || account.Name != "Алексей Пример" || account.ID != 999999999 {
		t.Fatalf("incorrect captured profile: %+v, %v", account, err)
	}
	for _, test := range []struct {
		name string
		body string
		id   int64
		fail bool
	}{
		{"current is not first", `{"status":"ok","result":{"count":2,"profiles":[{"profileId":111,"name":"Another account"},{"profileId":222,"name":"Current account","isCurrent":true}]}}`, 222, false},
		{"unnamed current account", `{"status":"ok","result":{"count":1,"profiles":[{"profileId":222,"isCurrent":true}]}}`, 222, false},
		{"no profiles", `{"status":"ok","result":{"count":0,"profiles":[]}}`, 0, false},
		{"no current profile", `{"status":"ok","result":{"count":1,"profiles":[{"profileId":111,"name":"Another account"}]}}`, 0, true},
		{"ambiguous current profile", `{"status":"ok","result":{"count":2,"profiles":[{"profileId":111,"isCurrent":true},{"profileId":222,"isCurrent":true}]}}`, 0, true},
		{"missing id", `{"status":"ok","result":{"count":1,"profiles":[{"name":"Example","isCurrent":true}]}}`, 0, true},
		{"missing count", `{"status":"ok","result":{"profiles":[]}}`, 0, true},
		{"null profiles", `{"status":"ok","result":{"count":0,"profiles":null}}`, 0, true},
		{"inconsistent count", `{"status":"ok","result":{"count":0,"profiles":[{"profileId":111,"isCurrent":true}]}}`, 0, true},
		{"error envelope", `{"status":"error","result":{"count":0,"profiles":[]}}`, 0, true},
		{"unsupported response", `{}`, 0, true},
		{"HTML challenge", `<html>Check your browser</html>`, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			account, err := parseAuthProfile([]byte(test.body))
			if (err != nil) != test.fail {
				t.Fatalf("unexpected parse result: %+v, %v", account, err)
			}
			if test.id == 0 && account != nil || test.id != 0 && (account == nil || account.ID != test.id) {
				t.Fatalf("incorrect account: %+v", account)
			}
		})
	}
}

func TestAuthChecksUseCandidateCookiesAndPreserveStoreOnFailure(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/web_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		code       int
		body       string
		headers    http.Header
		requestErr error
		verified   bool
		message    string
	}{
		{name: "signed in", code: 200, body: string(fixture), verified: true},
		{name: "signed out", code: 401, body: `{"error":{"message":"SYNTHETIC_PRIVATE_ERROR"}}`, verified: true, message: "Not signed in"},
		{name: "no profiles", code: 200, body: `{"status":"ok","result":{"count":0,"profiles":[]}}`, verified: true, message: "Not signed in"},
		{name: "forbidden", code: 403, message: "browser check"},
		{name: "challenge header", code: 200, body: string(fixture), headers: http.Header{"X-Firewall-Show-Captcha": {"true"}}, message: "browser check"},
		{name: "rate limited", code: 429, headers: http.Header{"Retry-After": {"120"}}, message: "temporarily limiting requests"},
		{name: "redirect", code: 302, headers: http.Header{"Location": {"https://outside.test/"}}, message: "could not check"},
		{name: "server error", code: 503, message: "could not check"},
		{name: "unexpected JSON", code: 200, body: `{}`, message: "unexpected response"},
		{name: "HTML", code: 200, body: `<html>SYNTHETIC_PRIVATE_ERROR</html>`, message: "unexpected response"},
		{name: "offline", requestErr: errors.New("SYNTHETIC_PRIVATE_ERROR"), message: "Could not reach Avito"},
		{name: "timeout", requestErr: context.DeadlineExceeded, message: "timed out"},
		{name: "canceled", requestErr: context.Canceled, message: "canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := testAuthStore(t)
			before, readErr := os.ReadFile(store.Path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			requests := 0
			httpClient := NewHTTPClient()
			httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.Method != http.MethodGet || req.URL.Host != "www.avito.ru" || req.URL.Path != authProfilesPath || req.URL.Query().Get("limit") != "3" || req.URL.Query().Get("withEntities") != "true" {
					t.Fatal("incorrect profile check request")
				}
				if req.Header.Get("Cookie") != "profile=SYNTHETIC_PROFILE; session=SYNTHETIC_NEW" || req.Header.Get("User-Agent") != "test-agent" || req.Header.Get("Accept") != "application/json" || req.Header.Get("Cache-Control") != "no-cache, no-store, max-age=0" {
					t.Fatal("incorrect auth check headers or candidate cookies")
				}
				during, readErr := os.ReadFile(store.Path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(before, during) {
					t.Fatal("import replaced credentials before verification")
				}
				if test.requestErr != nil {
					return nil, test.requestErr
				}
				return &http.Response{StatusCode: test.code, Header: test.headers.Clone(), Body: io.NopCloser(strings.NewReader(test.body)), Request: req}, nil
			})
			client, clientErr := NewClient(httpClient, store)
			if clientErr != nil {
				t.Fatal(clientErr)
			}
			status, err := client.ImportAuth(t.Context(), ChromeBrowser(), "Your Chrome", syntheticCookieReader(
				sweetcookie.Cookie{Name: "session", Value: "SYNTHETIC_NEW", Domain: "avito.ru", Path: "/"},
				sweetcookie.Cookie{Name: "profile", Value: "SYNTHETIC_PROFILE", Domain: "avito.ru", Path: authProfilesPath},
			), RequestOptions{UserAgent: "test-agent"})
			if requests != 1 || status.HTTPRequests != 1 || status.CheckedAt == nil || status.LoginVerified != test.verified {
				t.Fatalf("incorrect verification metadata: %+v, %v", status, err)
			}
			after, readErr := os.ReadFile(store.Path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if test.message == "" {
				if err != nil || !status.Imported || status.Account == nil || status.IsAuthenticated == nil || !*status.IsAuthenticated || len(status.Warnings) != 0 || bytes.Equal(before, after) {
					t.Fatalf("successful import was not saved: %+v, %v", status, err)
				}
				target, parseErr := url.Parse(WebOrigin + "/web/1/js/items")
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				cookies, err := store.cookiesFor(target)
				if err != nil || len(cookies) != 1 || cookies[0].Name != "session" {
					t.Fatal("profile cookie escaped its path")
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), test.message) || status.Imported || status.Account != nil || !bytes.Equal(before, after) || status.Error == "" {
					t.Fatalf("failed import did not preserve credentials: %+v, %v", status, err)
				}
				if test.verified && (status.IsAuthenticated == nil || *status.IsAuthenticated) || !test.verified && status.IsAuthenticated != nil {
					t.Fatalf("unknown verification was confused with signed out: %+v", status)
				}
				if test.requestErr != nil && !errors.Is(err, test.requestErr) {
					t.Fatal("lost transport error identity")
				}
			}
			if test.code == 429 {
				var failure *HTTPError
				if !errors.As(err, &failure) || failure.RetryAfter != "120" || status.RetryAfter != "120" {
					t.Fatal("lost rate limit retry hint")
				}
			}
			output, marshalErr := json.Marshal(status)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if strings.Contains(string(output), "SYNTHETIC_") {
				t.Fatal("auth output exposed credentials or raw server diagnostics")
			}
		})
	}
}

func TestAuthStatusIsReadOnlyAndDoesNotCheckUnavailableSessions(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("testdata/web_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		missing  bool
		expired  bool
		corrupt  bool
		requests int
	}{
		{name: "saved", requests: 1},
		{name: "missing", missing: true},
		{name: "expired", expired: true},
		{name: "corrupt", corrupt: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
			if !test.missing {
				store = testAuthStore(t)
			}
			if test.expired {
				record, loadErr := store.load()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				expired := time.Now().Add(-time.Hour)
				record.Cookies[0].Expires = &expired
				if err := store.save(t.Context(), record); err != nil {
					t.Fatal(err)
				}
			}
			if test.corrupt {
				if err := os.WriteFile(store.Path, []byte("SYNTHETIC_BAD_STORE"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := os.ReadFile(store.Path)
			if test.missing && !errors.Is(readErr, os.ErrNotExist) || !test.missing && readErr != nil {
				t.Fatal(readErr)
			}
			requests := 0
			httpClient := NewHTTPClient()
			httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(fixture)), Request: req}, nil
			})
			client, clientErr := NewClient(httpClient, store)
			if clientErr != nil {
				t.Fatal(clientErr)
			}
			status, err := client.AuthStatus(t.Context(), RequestOptions{})
			if requests != test.requests || status.HTTPRequests != test.requests || (err == nil) != (test.requests == 1) {
				t.Fatalf("unexpected auth status: %+v, %v", status, err)
			}
			after, readErr := os.ReadFile(store.Path)
			if !bytes.Equal(before, after) || test.missing && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatal("auth status changed the credential store")
			}
		})
	}
}

func TestCanceledVerificationNeverSavesImportedSession(t *testing.T) {
	t.Parallel()

	store := testAuthStore(t)
	before, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	httpClient := NewHTTPClient()
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"status":"ok","result":{"count":1,"profiles":[{"profileId":222,"isCurrent":true}]}}`)), Request: req}, nil
	})
	client, clientErr := NewClient(httpClient, store)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	status, err := client.ImportAuth(ctx, ChromeBrowser(), "", syntheticCookieReader(sweetcookie.Cookie{Name: "session", Value: "SYNTHETIC_NEW", Domain: "avito.ru", Path: "/"}), RequestOptions{})
	after, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !errors.Is(err, context.Canceled) || status.Imported || !bytes.Equal(before, after) {
		t.Fatalf("canceled check saved credentials: %+v, %v", status, err)
	}
}
