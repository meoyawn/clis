package cli

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestMainHTTPWorkflow(t *testing.T) {
	serviceFixture, err := os.ReadFile("testdata/main_services.html")
	if err != nil {
		t.Fatal(err)
	}
	counterFixture, err := os.ReadFile("testdata/main_counters.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                                                                                                  string
		changed, dry, wrongAccount, wrongFlat, wrongService, decrease, fraction, dropWrite, network, renumber bool
		wantSelections, wantPosts                                                                             int
		wantError                                                                                             string
	}{
		{name: "unchanged normal run skips submission", wantSelections: 1},
		{name: "unchanged dry run", dry: true, wantSelections: 1},
		{name: "changed dry run", changed: true, dry: true, wantSelections: 1},
		{name: "submit all values and preserve hidden metadata", changed: true, wantSelections: 1, wantPosts: 1},
		{name: "fresh dynamic meter IDs", changed: true, renumber: true, wantSelections: 1, wantPosts: 1},
		{name: "wrong account stops before service selection", wrongAccount: true, wantError: "different main account"},
		{name: "wrong apartment stops before service selection", wrongFlat: true, wantError: "expected address"},
		{name: "wrong service", wrongService: true, wantSelections: 1, wantError: "unsupported main service"},
		{name: "lower than current portal reading", decrease: true, wantSelections: 1, wantError: "below portal"},
		{name: "fractional portal value is not truncated", fraction: true, wantSelections: 1, wantError: "below portal"},
		{name: "POST response echo is not proof of persistence", changed: true, dropWrite: true, wantSelections: 1, wantPosts: 1, wantError: "read-back mismatch"},
		{name: "uncertain POST is never replayed", changed: true, network: true, wantSelections: 1, wantPosts: 1, wantError: "outcome is unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := mainReading{Routing: mainAccount{Account: "0000000001", Flat: "1", Address: "ул ПРИМЕРНАЯ, дом 1", Informer: "00000000-0000-4000-8000-000000000001"}, Date: time.Date(2000, 10, 18, 0, 0, 0, 0, time.UTC), Electricity: 1100, Hot: 110, Cold: 200}
			if test.changed {
				r.Electricity = 1101
				r.Hot = 111
			}
			if test.decrease {
				r.Electricity = 1099
			}
			services, counters := string(serviceFixture), string(counterFixture)
			if test.wrongAccount {
				services = strings.ReplaceAll(services, "0000000001", "0000000002")
			}
			if test.wrongFlat {
				services = strings.ReplaceAll(services, "квар. 1", "квар. 2")
			}
			if test.wrongService {
				counters = strings.ReplaceAll(counters, "Электроснабжение день", "Электроснабжение ночь")
			}
			ids := []string{"100000001", "100000002", "100000003"}
			if test.renumber {
				counters = strings.NewReplacer(ids[0], "999000003", ids[1], "999000001", ids[2], "999000002").Replace(counters)
				ids = []string{"999000003", "999000001", "999000002"}
			}
			current := map[string]string{ids[0]: "1100.000", ids[1]: "110.000", ids[2]: "200.000"}
			if test.fraction {
				current[ids[0]] = "1100.125"
			}
			old := []string{"1000.000", "100.000", "200.000"}
			initial := []string{"1100.000", "110.000", "200.000"}
			wanted := []string{"1101.000", "111.000", "200.000"}
			bodyWithValues := func(values map[string]string) string {
				body := counters
				for i, id := range ids {
					body = strings.Replace(body, `name="value[`+id+`]" placeholder="0.000" size="30" type="text" value="`+initial[i]+`"`, `name="value[`+id+`]" placeholder="0.000" size="30" type="text" value="`+values[id]+`"`, 1)
				}
				return body
			}
			selections, posts, readsAfterPost := 0, 0, 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				cookie, _ := req.Cookie("session")
				if cookie == nil || cookie.Value != "fake-session" {
					http.Error(w, "unauthenticated", http.StatusUnauthorized)
					return
				}
				switch req.URL.Path {
				case "/personal-account/init-by-user-tsj":
					if req.URL.Query().Get("informer_id") != r.Routing.Informer || req.URL.Query().Get("informer_type") != "jkh_payment" {
						t.Error("incorrect saved apartment routing")
						http.Error(w, "bad routing", 400)
						return
					}
					fmt.Fprint(w, `<a href="/personal-account/choise?globalsid=test-session">Услуги по лицевому счету</a>`)
				case "/personal-account/choise":
					fmt.Fprint(w, services)
				case "/personal_account/choise":
					selections++
					if req.FormValue("serviceType") != "counter/value?update" || req.FormValue("__csrf_test") != "test-token" || req.FormValue("globalsid") != "test-token" {
						t.Error("service selection or fresh CSRF/session fields differ from HAR")
						http.Error(w, "bad service choice", 400)
						return
					}
					http.Redirect(w, req, "/personal-account/counter/value?update&globalsid=test-session", http.StatusFound)
				case "/personal-account/counter/value":
					if posts > 0 {
						readsAfterPost++
					}
					fmt.Fprint(w, bodyWithValues(current))
				case "/personal_account/counter/value":
					posts++
					if test.network {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
						t.Error("unexpected submission transport")
					}
					if err := req.ParseForm(); err != nil {
						t.Error(err)
					}
					if len(req.PostForm) != 14 || req.FormValue("__csrf_test") != "test-token" || req.FormValue("globalsid") != "test-token" {
						t.Error("submission field set or CSRF/session token differs from browser form")
						http.Error(w, "bad form", 400)
						return
					}
					for i, id := range ids {
						itemPattern := regexp.MustCompile(`name="item\[` + id + `\]"[^>]*value="([^"]*)"`)
						item := html.UnescapeString(itemPattern.FindStringSubmatch(counters)[1])
						if req.FormValue("value["+id+"]") != wanted[i] || req.FormValue("old["+id+"]") != old[i] || req.FormValue("item["+id+"]") != item || !req.PostForm.Has("check["+id+"]") || req.FormValue("check["+id+"]") != "" {
							t.Errorf("meter %s submission changed serialized metadata, old/check fields or has wrong value", id)
							http.Error(w, "bad meter form", 400)
							return
						}
					}
					if test.dropWrite {
						echo := map[string]string{}
						for _, id := range ids {
							echo[id] = req.FormValue("value[" + id + "]")
						}
						fmt.Fprint(w, bodyWithValues(echo))
						return
					}
					for _, id := range ids {
						current[id] = req.FormValue("value[" + id + "]")
					}
					http.Redirect(w, req, "/personal-account/counter/value?globalsid=test-session", http.StatusFound)
				default:
					http.NotFound(w, req)
				}
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			origin, _ := url.Parse(server.URL)
			jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "fake-session", Path: "/"}})
			client, err := newPortalClient(server.URL, &http.Client{Jar: jar, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = client.submitMain(context.Background(), r, test.dry, &out)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("want %q, got %v", test.wantError, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if selections != test.wantSelections || posts != test.wantPosts {
				t.Fatalf("service/meter POSTs %d/%d, want %d/%d", selections, posts, test.wantSelections, test.wantPosts)
			}
			if test.wantError != "" {
				return
			}
			if test.dry && !strings.Contains(out.String(), "no electricity or water readings submitted") {
				t.Errorf("missing dry-run confirmation: %s", &out)
			}
			if !test.dry && !test.changed && !strings.Contains(out.String(), "already match the portal; nothing submitted") {
				t.Errorf("missing no-op confirmation: %s", &out)
			}
			if test.changed && !test.dry && (readsAfterPost < 2 || !strings.Contains(out.String(), "submitted and verified")) {
				t.Errorf("missing independent read-back verification: %s", &out)
			}
		})
	}
}
