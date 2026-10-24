package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSubmissionHTTPContract(t *testing.T) {
	fixture, err := os.ReadFile("testdata/counter.html")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("testdata/submission.txt")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := url.ParseQuery(strings.TrimSpace(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	zone, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 10, 19, 21, 0, 0, 0, zone)
	readings := reading{Date: now, Day: 2060, Night: 320, Water: 102, Drinking: 405}
	credentialsPath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(credentialsPath, []byte(`{
		"login": "test@example.com", "password": "test-password",
		"account": "0000000001",
		"meters": {"water": "0000000000001", "day": "К-000000001-Э", "night": "К-000000001-Э", "drinking": "0000001"}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := readCredentials(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                                        string
		dryRun, expireOnce, expireAlways, dropWrite bool
		wrongMeter, lowerReading, oldDate           bool
		wrongAccount, missingAccount, missingMeter  bool
		networkFailure, reject                      bool
		wantPosts, wantLogins                       int
		wantError                                   string
	}{
		{name: "submit and read back", wantPosts: 1, wantLogins: 1},
		{name: "dry run never posts readings", dryRun: true, wantLogins: 1},
		{name: "expired POST refreshes login and IDs", expireOnce: true, wantPosts: 2, wantLogins: 2},
		{name: "repeated expiry stops", expireAlways: true, wantPosts: 2, wantLogins: 2, wantError: "session expired twice"},
		{name: "success banner without persistence", dropWrite: true, wantPosts: 1, wantLogins: 1, wantError: "read-back verification failed"},
		{name: "saved values with stale month", oldDate: true, wantPosts: 1, wantLogins: 1, wantError: "read-back verification failed"},
		{name: "unexpected meter", wrongMeter: true, wantLogins: 1, wantError: "expected one meter"},
		{name: "unexpected account never posts", wrongAccount: true, wantLogins: 1, wantError: "does not match the configured account"},
		{name: "missing account stops before login", missingAccount: true, wantError: "credentials require nonempty account"},
		{name: "missing meter stops before login", missingMeter: true, wantError: "credentials require nonempty meters.water"},
		{name: "lower reading", lowerReading: true, wantLogins: 1, wantError: "below BRIS reading"},
		{name: "uncertain network result is not replayed", networkFailure: true, wantPosts: 1, wantLogins: 1, wantError: "outcome is unknown"},
		{name: "rejection is not replayed", reject: true, wantPosts: 1, wantLogins: 1, wantError: "did not confirm acceptance"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logins, posts, readsAfterWrite := 0, 0, 0
			stored := map[string]string{}
			var session string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if r.URL.Path == "/MILLENIUM2/Account/LogOn" {
					if r.Method == http.MethodGet {
						http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "login-cookie", Path: "/"})
						fmt.Fprint(w, `<form method="post" action="/MILLENIUM2/Account/LogOn"><input name="Login"><input name="Password" type="password"><input type="hidden" name="csrf" value="login-token"></form>`)
						return
					}
					csrf, _ := r.Cookie("csrf")
					if r.FormValue("Login") != "test@example.com" || r.FormValue("Password") != "test-password" || r.FormValue("RememberMe") != "true" || r.FormValue("csrf") != "login-token" || csrf == nil {
						t.Error("login did not carry credentials, remember-me, hidden token and session cookie")
						http.Error(w, "invalid login", http.StatusBadRequest)
						return
					}
					logins++
					session = fmt.Sprint(logins)
					http.SetCookie(w, &http.Cookie{Name: "session", Value: session, Path: "/", HttpOnly: true})
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
				if r.URL.Path == "/" {
					fmt.Fprint(w, "Logged in")
					return
				}
				if r.URL.Path != counterPath {
					http.NotFound(w, r)
					return
				}
				cookie, _ := r.Cookie("session")
				if session == "" || cookie == nil || cookie.Value != session {
					http.Redirect(w, r, "/MILLENIUM2/Account/LogOn?ReturnUrl=%2FMILLENIUM2%2FHome%2FCounter", http.StatusFound)
					return
				}
				if r.Method == http.MethodPost {
					posts++
					if test.expireAlways || (test.expireOnce && posts == 1) {
						session = ""
						http.Redirect(w, r, "/MILLENIUM2/Account/LogOn", http.StatusFound)
						return
					}
					if test.networkFailure {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					want := url.Values{}
					for key, values := range expected {
						want[key] = append([]string(nil), values...)
					}
					if logins > 1 {
						want.Set("counters[0].ACounterId", "101001")
					}
					if !reflect.DeepEqual(r.PostForm, want) {
						t.Errorf("submission differs from HAR contract: got %v, want %v", r.PostForm, want)
						http.Error(w, "invalid form", http.StatusBadRequest)
						return
					}
					if test.reject {
						fmt.Fprint(w, `<div class="validation-summary-errors">Rejected</div>`)
						return
					}
					if !test.dropWrite {
						for _, index := range []string{"0", "1", "2", "3"} {
							stored[index] = r.PostForm.Get("counters[" + index + "].Value")
						}
					}
					http.Redirect(w, r, counterPath+"?accepted=true", http.StatusFound)
					return
				}
				body := string(fixture)
				if logins > 1 {
					body = strings.ReplaceAll(body, `value="100001"`, `value="101001"`)
				}
				if test.wrongMeter {
					body = strings.ReplaceAll(body, "0000000000001", "unrelated-meter")
				}
				if test.lowerReading {
					body = strings.ReplaceAll(body, "100", "9999")
				}
				for index, old := range map[string]string{"0": "100", "1": "2000", "2": "300", "3": "400"} {
					if value, ok := stored[index]; ok {
						body = strings.ReplaceAll(body, "\n"+old+"\n", "\n"+value+"\n")
					}
				}
				if len(stored) > 0 && !test.oldDate {
					body = strings.ReplaceAll(body, "25 августа 2030", "19 октября 2030")
				}
				if r.URL.Query().Get("accepted") == "true" {
					body = `<div class="alert alert-success">Введенные показания счетчиков приняты.</div>` + body
				} else if posts > 0 {
					readsAfterWrite++
				}
				fmt.Fprint(w, body)
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			configured := creds
			if test.wrongAccount {
				configured.Account = "0000000002"
			}
			if test.missingAccount {
				configured.Account = ""
			}
			if test.missingMeter {
				configured.Meters.Water = ""
			}
			client, err := newBrisClient(server.URL, configured)
			var output bytes.Buffer
			if err == nil {
				err = client.submit(context.Background(), readings, now, test.dryRun, &output)
			}
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected error containing %q, got %v", test.wantError, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if posts != test.wantPosts || logins != test.wantLogins {
				t.Errorf("meter POSTs/logins = %d/%d, want %d/%d", posts, logins, test.wantPosts, test.wantLogins)
			}
			if test.wantError == "" && !test.dryRun && (readsAfterWrite == 0 || !strings.Contains(output.String(), "accepted and verified all 4")) {
				t.Errorf("submission did not independently read back and confirm stored readings: %s", &output)
			}
			if test.dryRun && !strings.Contains(output.String(), "Dry run: no meter readings submitted") {
				t.Errorf("missing dry-run result: %s", &output)
			}
		})
	}
}
