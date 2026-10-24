package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	avito "github.com/meoyawn/clis/avito/internal/service"
	"github.com/steipete/sweetcookie"
)

func TestAuthCommandsShowAccountWithJSONOnlyWhenRequested(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile("../service/testdata/web_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	auth := avito.AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	requests, imports := 0, 0
	httpClient := avito.NewHTTPClient()
	httpClient.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Path != "/web/1/profiles/list" {
			t.Fatal("auth command did not request the current profile")
		}
		if req.Header.Get("User-Agent") != "auth-test-agent" {
			t.Fatal("auth command ignored --user-agent")
		}
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("auth command ignored --timeout")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(fixture)), Request: req}, nil
	})
	client, clientErr := avito.NewClient(httpClient, auth)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	setup := func() (dependencies, error) {
		return dependencies{client: client, readCookies: func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
			imports++
			return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "session", Value: "SYNTHETIC_SECRET", Domain: "avito.ru", Path: "/"}}}, nil
		}}, nil
	}
	for i, test := range []struct {
		args []string
		want string
		json bool
	}{
		{args: []string{"auth", "import", "--browser", "chrome"}, want: "Signed in as Алексей Пример. Session saved.\n"},
		{args: []string{"auth", "status"}, want: "Signed in as Алексей Пример.\n"},
		{args: []string{"auth", "status", "--json=false"}, want: "Signed in as Алексей Пример.\n"},
		{args: []string{"auth", "status", "--json"}, json: true},
	} {
		var stdout, stderr bytes.Buffer
		args := append(slices.Clone(test.args), "--timeout", "2s", "--user-agent", "auth-test-agent")
		if err := run(t.Context(), args, &stdout, &stderr, setup, testConfigStore(t)); err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 || requests != i+1 || imports != 1 {
			t.Fatalf("unexpected auth side effects: requests=%d imports=%d stderr=%q", requests, imports, stderr.String())
		}
		if test.json {
			var status avito.AuthStatus
			if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.Account == nil || status.Account.Name != "Алексей Пример" || status.Error != "" || !status.LoginVerified {
				t.Fatalf("invalid JSON auth output: %s, %v", stdout.String(), err)
			}
		} else if stdout.String() != test.want {
			t.Fatalf("want %q, got %q", test.want, stdout.String())
		}
		if strings.Contains(stdout.String(), "SYNTHETIC_SECRET") {
			t.Fatal("auth output exposed session credentials")
		}
	}
}

func TestAuthFailureHasActionableTextAndNonzeroResult(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		missing bool
		json    bool
		code    int
		want    string
	}{
		{name: "not imported", missing: true, want: "Not signed in"},
		{name: "signed out", code: 401, want: "Not signed in"},
		{name: "browser check", code: 403, want: "browser check"},
		{name: "JSON failure", code: 401, json: true, want: "Not signed in"},
		{name: "JSON missing", missing: true, json: true, want: "Not signed in"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			auth := avito.AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
			if !test.missing {
				if _, err := auth.Import(t.Context(), avito.FirefoxBrowser(), "Work profile", func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
					return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "session", Value: "SYNTHETIC_SECRET", Domain: "avito.ru", Path: "/"}}}, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			requests := 0
			httpClient := avito.NewHTTPClient()
			httpClient.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: test.code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"SYNTHETIC_PRIVATE_ERROR"}}`)), Request: req}, nil
			})
			client, clientErr := avito.NewClient(httpClient, auth)
			if clientErr != nil {
				t.Fatal(clientErr)
			}
			setup := func() (dependencies, error) {
				return dependencies{client: client, readCookies: func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
					t.Fatal("status reopened the browser cookie database")
					return sweetcookie.Result{}, nil
				}}, nil
			}
			args := []string{"auth", "status"}
			if test.json {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			err := run(t.Context(), args, &stdout, &stderr, setup, testConfigStore(t))
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "avito auth import --browser") {
				t.Fatalf("auth failure lacks a useful next step: %v", err)
			}
			if !test.missing && !strings.Contains(err.Error(), "--browser firefox --browser-profile 'Work profile'") {
				t.Fatalf("lost the selected browser/profile in recovery instructions: %v", err)
			}
			if test.missing && requests != 0 || !test.missing && requests != 1 {
				t.Fatalf("unexpected request count: %d", requests)
			}
			if test.json {
				var status avito.AuthStatus
				if json.Unmarshal(stdout.Bytes(), &status) != nil || status.Error == "" || status.Account != nil || status.HTTPRequests != requests {
					t.Fatalf("invalid JSON failure: %s", stdout.String())
				}
			} else if stdout.Len() != 0 {
				t.Fatalf("auth failure printed success output: %q", stdout.String())
			}
			if strings.Contains(err.Error()+stdout.String()+stderr.String(), "SYNTHETIC_") {
				t.Fatal("auth failure exposed credentials or raw server diagnostics")
			}
		})
	}
}
