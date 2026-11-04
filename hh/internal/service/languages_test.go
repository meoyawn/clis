package service_test

import (
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

	hh "github.com/meoyawn/clis/hh/internal/service"
)

// Language edits intentionally propagate across resumes, unlike content edits.
// This owns the shared write, technical-level preservation and real print list.
func TestSharedLanguagesUpdateVerifiesAllResumeLanguageLists(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "stale-print", "changed-technical-level", "changed-profile", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			languages := []any{hh.Object{"id": "71", "degree": "c2"}, hh.Object{"id": "1", "degree": "c1"}}
			posts := 0
			fields := hh.Object{"language": languages, "firstName": []any{hh.Object{"string": "Synthetic"}}}
			profile := hh.Object{"profile": hh.Object{"fields": fields}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/shards/applicant/profile/get_full_data":
					_ = json.NewEncoder(w).Encode(profile)
				case "/shards/applicant/profile/update":
					posts++
					if r.Method != http.MethodPost || r.Header.Get("X-Xsrftoken") != "fixture-token" || r.URL.RawQuery != "" {
						t.Error("language edit lost native shared-profile contract")
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "120")
						w.WriteHeader(429)
						return
					}
					var body hh.Object
					_ = json.NewDecoder(r.Body).Decode(&body)
					patch := body["profile"].(map[string]any)
					if len(body) != 1 || len(patch) != 1 || patch["language"] == nil {
						t.Errorf("language write included unrelated fields: %v", body)
					}
					languages = patch["language"].([]any)
					fields["language"] = languages
					if scenario == "changed-profile" {
						fields["firstName"] = []any{hh.Object{"string": "Wrong"}}
					}
					_ = json.NewEncoder(w).Encode(profile)
				case "/shards/applicant/resumes":
					items := []any{}
					for _, hash := range hashes {
						items = append(items, hh.Object{"_attributes": hh.Object{"hash": hash}})
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"resumes": items})
				case "/applicant/resume":
					technicalLevel := "advanced"
					if posts > 0 && scenario == "changed-technical-level" && r.URL.Query().Get("resume") == hashes[2] {
						technicalLevel = "base"
					}
					inventory := []any{hh.Object{"id": 101, "name": "Go", "category": "SKILL", "level": hh.Object{"internalId": technicalLevel}}}
					for _, value := range languages {
						row := value.(map[string]any)
						name, label := "Русский", "L1 - Родной"
						if row["id"] == "1" {
							name, label = "Английский", "C1 — Продвинутый"
						}
						inventory = append(inventory, hh.Object{"id": row["id"], "name": name, "category": "LANG", "level": hh.Object{"internalId": row["degree"], "name": label}})
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"_attributes": hh.Object{"hash": r.URL.Query().Get("resume")}, "title": []any{hh.Object{"string": "Unchanged engineer"}}, "language": languages, "resumeApplicantSkills": inventory})
				default:
					if !strings.HasPrefix(r.URL.Path, "/resume/") {
						t.Errorf("unexpected request %s %s", r.Method, r.URL)
						http.NotFound(w, r)
						return
					}
					hash := strings.TrimPrefix(r.URL.Path, "/resume/")
					native := "Русский"
					if scenario == "stale-print" && hash == hashes[2] {
						native = "Татарский"
					}
					fmt.Fprintf(w, `<div id="HH-React-Root"><div data-qa="resume-languages-block"><div data-qa="resume-block-language-item">%s</div><div data-qa="resume-block-language-item">Английский — C1 — Продвинутый</div></div></div><template id="HH-Lux-InitialState">%s</template>`, native, html.EscapeString(fmt.Sprintf(`{"resume":{"hash":%q}}`, hash)))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(base, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			httpClient.Jar = jar
			client := hh.NewClient(httpClient, base)
			var patch hh.Object
			_ = json.Unmarshal([]byte(`{"language":[{"id":71,"degree":"l1"},{"id":1,"degree":"c1"}]}`), &patch)
			result, err := client.UpdateLanguages(t.Context(), patch, scenario != "preview", t.TempDir())
			if scenario == "preview" {
				if err != nil || posts != 0 || result["applied"] != false {
					t.Fatalf("preview wrote or failed: %v %v posts=%d", result, err, posts)
				}
				return
			}
			if scenario != "apply" {
				if err == nil || posts != 1 {
					t.Fatalf("unsafe update succeeded or retried: %v %v posts=%d", result, err, posts)
				}
				return
			}
			if err != nil || posts != 1 || result["verified"] != true || len(result["affected_resumes"].([]any)) != len(hashes) {
				t.Fatalf("not all language print views verified: %v %v posts=%d", result, err, posts)
			}
			info, err := os.Stat(result["backup_path"].(string))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("private backup missing: %v %v", info, err)
			}
		})
	}
}
