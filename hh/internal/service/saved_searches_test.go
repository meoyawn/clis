package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

// Synthetic records retain the native favorites and save transport contracts.
// No real account IDs, email addresses, cookies or browser captures are fixtures.
func TestSavedSearchLifecycle(t *testing.T) {
	const queryText = "NAME:(Go OR Golang)"
	for _, scenario := range []string{"preview", "create", "existing", "wrong-criteria", "missing", "changed-existing", "unverified-email", "post-failure", "incomplete-list"} {
		t.Run(scenario, func(t *testing.T) {
			reads, posts := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/applicant/favorites":
					reads++
					if r.URL.RawQuery != "tab=searches" {
						t.Errorf("wrong native saved-search tab: %s", r.URL)
					}
					previous := hh.Object{
						"id": "101", "name": "Older search", "emailActive": false,
						"searchArgs":       "text=erlang&amp;area=1624&amp;saved_search_id=101&amp;no_magic=true",
						"param":            hh.Object{"text": "erlang", "area": []any{"1624"}},
						"deactivationhash": "private-unused-value",
					}
					created := hh.Object{
						"id": "202", "name": queryText, "emailActive": true,
						"searchArgs": "text=NAME%3A%28Go+OR+Golang%29&amp;work_format=REMOTE&amp;items_on_page=20&amp;saved_search_id=202&amp;no_magic=true",
						"param":      hh.Object{"text": queryText, "work_format": []any{"REMOTE"}},
					}
					rows := []any{previous}
					if scenario == "existing" || posts > 0 && scenario != "missing" {
						rows = append(rows, created)
					}
					if scenario == "existing" {
						created["emailActive"] = false
					}
					if posts > 0 && scenario == "wrong-criteria" {
						created["searchArgs"] = created["searchArgs"].(string) + "&amp;area=88"
					}
					if posts > 0 && scenario == "changed-existing" {
						previous["emailActive"] = true
					}
					data := hh.Object{
						"email": "candidate@example.test", "emailVerified": scenario != "unverified-email",
						"savedSearch": rows, "totalItems": len(rows), "paging": nil,
					}
					if scenario == "incomplete-list" {
						data["totalItems"] = 3
						data["paging"] = hh.Object{"next": 1}
					}
					state, _ := json.Marshal(hh.Object{"favoritesVacancySavedSearches": data})
					fmt.Fprintf(w, `<html><template id="HH-Lux-InitialState">%s</template></html>`, state)
				case r.Method == http.MethodPost && r.URL.Path == "/vacancysavedsearch/save":
					posts++
					want := url.Values{"text": {queryText}, "work_format": {"REMOTE"}, "items_on_page": {"20"}, "enable_snippets": {"true"}, "ored_clusters": {"true"}}
					if !reflect.DeepEqual(r.URL.Query(), want) {
						t.Errorf("save criteria changed or inherited an area/salary filter: %v", r.URL.Query())
					}
					if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Xsrftoken") != "fixture-token" {
						t.Errorf("native write headers missing: %v", r.Header)
					}
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode save body: %v", err)
						http.Error(w, "invalid save body", http.StatusBadRequest)
						return
					}
					if !reflect.DeepEqual(body, map[string]string{"email": "candidate@example.test"}) {
						t.Errorf("wrong native save body: %v", body)
					}
					if scenario == "post-failure" {
						http.Error(w, `{"error":"temporary_failure"}`, http.StatusServiceUnavailable)
						return
					}
					// A successful POST alone must never establish a saved search.
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(base, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			client := server.Client()
			client.Jar = jar
			result, err := hh.NewClient(client, base).SaveSearch(context.Background(), domain.SavedSearchOptions{Text: queryText, Remote: true}, scenario != "preview")
			if posts > 1 {
				t.Fatalf("save was retried: %d POSTs", posts)
			}
			switch scenario {
			case "preview":
				if err != nil || posts != 0 || reads != 1 || result.Created || result.Verified || result.Search != nil {
					t.Fatalf("preview changed account or claimed persistence: result=%+v err=%v reads=%d posts=%d", result, err, reads, posts)
				}
			case "existing":
				if err != nil || posts != 0 || reads != 1 || result.Created || !result.Verified || result.Search.ID != "202" || result.Search.EmailActive {
					t.Fatalf("existing search was duplicated or its notification setting changed: result=%+v err=%v", result, err)
				}
			case "create":
				if err != nil || posts != 1 || reads != 2 || !result.Created || !result.Verified || result.Search.ID != "202" {
					t.Fatalf("saved criteria were not independently verified: result=%+v err=%v reads=%d posts=%d", result, err, reads, posts)
				}
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), "deactivationhash") || strings.Contains(string(encoded), "private-unused-value") {
					t.Fatalf("private subscription token leaked: %s", encoded)
				}
			default:
				if err == nil || result != nil {
					t.Fatalf("unverified save accepted: result=%+v err=%v", result, err)
				}
				if scenario == "unverified-email" || scenario == "incomplete-list" {
					if posts != 0 || reads != 1 {
						t.Fatalf("invalid initial state reached mutation: reads=%d posts=%d", reads, posts)
					}
				} else if scenario == "post-failure" {
					if posts != 1 || reads != 1 || !strings.Contains(err.Error(), "inspect saved-searches list") {
						t.Fatalf("uncertain write retried or not identified: reads=%d posts=%d err=%v", reads, posts, err)
					}
				} else if posts != 1 || reads != 2 {
					t.Fatalf("wrong readback lifecycle: reads=%d posts=%d", reads, posts)
				}
			}
		})
	}
}
