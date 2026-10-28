package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

type duplicateTransport func(*http.Request) (*http.Response, error)

func (f duplicateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The fixture follows the independently observed {hash,userPrint} -> {url}
// contract. Assertions guard the public write boundary and account isolation;
// no live clone, publication or job application is used to test this behavior.
func TestDuplicateResumeBindsReceiptAndPreservesExistingResumes(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "wrong-receipt", "ambiguous-new-ids", "changed-existing", "rate-limit", "foreign-source", "stale-metadata"} {
		t.Run(scenario, func(t *testing.T) {
			posts := 0
			reads := 0
			created := false
			resumes := map[string]hh.Object{}
			for _, hash := range hashes[:2] {
				resumes[hash] = hh.Object{
					"_attributes": hh.Object{"hash": hash, "permission": "edit", "status": "modified"},
					"title":       []any{hh.Object{"string": "Original engineer"}},
					"accessType":  []any{hh.Object{"string": "clients"}},
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					reads++
				}
				switch r.URL.Path {
				case "/shards/applicant/resumes":
					items := []any{hh.Object{"hash": hashes[1]}, hh.Object{"hash": hashes[0]}}
					if created {
						items = append(items, hh.Object{"hash": hashes[2]})
						if scenario == "ambiguous-new-ids" {
							items = append(items, hh.Object{"hash": strings.Repeat("d", 38)})
						}
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"resumes": items})
				case "/applicant/resume":
					_ = json.NewEncoder(w).Encode(resumes[r.URL.Query().Get("resume")])
				case "/profile/shards/resume/clone":
					posts++
					if r.Method != http.MethodPost || r.Header.Get("X-Xsrftoken") != "fixture-token" || r.Header.Get("User-Agent") != "Captured browser fixture" {
						t.Errorf("invalid authenticated clone request: method=%s", r.Method)
					}
					var body hh.Object
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					fingerprints, _ := body["userPrint"].(map[string]any)
					if len(body) != 2 || body["hash"] != hashes[0] || fingerprints["fingerprintIteration2"] != "captured-one" || fingerprints["fingerprintSp"] != "captured-two" {
						t.Errorf("clone request differs from native contract: %v", body)
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(http.StatusTooManyRequests)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					created = true
					resumes[hashes[2]] = hh.Object{
						"_attributes": hh.Object{"hash": hashes[2], "permission": "edit", "status": "incomplete"},
						"title":       []any{hh.Object{"string": "Original engineer"}},
						"accessType":  []any{hh.Object{"string": "no_one"}},
					}
					if scenario == "changed-existing" {
						resumes[hashes[1]]["title"] = []any{hh.Object{"string": "Unexpected edit"}}
					}
					receiptID := hashes[2]
					if scenario == "wrong-receipt" {
						receiptID = hashes[0]
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"url": "/resume/" + receiptID})
				default:
					hash := strings.TrimPrefix(r.URL.Path, "/resume/")
					if r.Method != http.MethodGet || hash == r.URL.Path || r.URL.Query().Get("print") != "true" {
						t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL)
						http.NotFound(w, r)
						return
					}
					state, _ := json.Marshal(hh.Object{"resume": hh.Object{"hash": hash}})
					fmt.Fprintf(w, `<div id="HH-React-Root">Original engineer</div><template id="HH-Lux-InitialState">%s</template>`, html.EscapeString(string(state)))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			cloneURL, _ := url.Parse("https://resume-profile-front.hh.ru")
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(cloneURL, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			nativeTransport := httpClient.Transport
			httpClient.Jar = jar
			httpClient.Transport = duplicateTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == cloneURL.Host {
					if r.Header.Get("Origin") != server.URL || r.Header.Get("Referer") != server.URL+"/applicant/profile/me" {
						t.Error("clone lost browser origin/referer")
					}
					local := r.Clone(r.Context())
					local.URL.Scheme, local.URL.Host = base.Scheme, base.Host
					return nativeTransport.RoundTrip(local)
				}
				return nativeTransport.RoundTrip(r)
			})
			client := hh.NewClient(httpClient, base)
			id, _ := hh.NewResumeIDFromValue(hashes[0])
			if scenario == "foreign-source" {
				id, _ = hh.NewResumeIDFromValue(strings.Repeat("f", 38))
			}
			metadata := hh.CloneMetadata{Origin: server.URL, UserAgent: "Captured browser fixture", CapturedAt: time.Now(), UserPrint: map[string]string{"fingerprintIteration2": "captured-one", "fingerprintSp": "captured-two"}}
			if scenario == "stale-metadata" {
				metadata.CapturedAt = time.Now().Add(-48 * time.Hour)
			}
			plan, err := client.Duplicate(context.Background(), id, metadata, scenario != "preview", t.TempDir())
			wantError := map[string]string{"wrong-receipt": "exactly one new public", "ambiguous-new-ids": "exactly one new public", "changed-existing": "ISOLATION FAILURE", "rate-limit": "rate_limited", "foreign-source": "not in the selected account", "stale-metadata": "older than 24 hours"}[scenario]
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("want %q, got %v", wantError, err)
				}
				wantPosts := 1
				if scenario == "foreign-source" || scenario == "stale-metadata" {
					wantPosts = 0
				}
				if posts != wantPosts {
					t.Fatalf("expected %d writes, got %d", wantPosts, posts)
				}
				if scenario == "stale-metadata" && reads != 0 {
					t.Fatal("invalid metadata made network requests")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview" {
				if posts != 0 || plan.Applied || plan.Verified || plan.ID != "" {
					t.Fatalf("preview wrote or invented a clone: %+v", plan)
				}
				return
			}
			if posts != 1 || !plan.Applied || !plan.Verified || plan.ID != hashes[2] || len(plan.OtherResumesUnchanged) != 2 || plan.Status != "incomplete" {
				t.Fatalf("clone verification incomplete: %+v, posts=%d", plan, posts)
			}
			stat, err := os.Stat(plan.BackupPath)
			if err != nil || stat.Mode().Perm() != 0600 {
				t.Fatalf("private backup missing or unsafe: %v", err)
			}
		})
	}
}
