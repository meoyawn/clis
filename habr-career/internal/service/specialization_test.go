package service_test

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/service"
)

// Owns the specialization form's ordered Rails rows and independent public
// readback. Existing experience tests do not exercise either contract.
func TestSpecializationUpdatePreservesMetadataAndPairsLanguages(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "reordered-languages", "stale-print", "extra-print-language", "metadata-drift", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			const alias = "synthetic-user"
			headline, posts := "Old headline", 0
			languages := []any{service.Object{"languageId": 71, "gradeId": 6, "title": "Русский С2"}, service.Object{"languageId": 1, "gradeId": 5, "title": "Английский С1"}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/frontend_v1/users/me":
					_ = json.NewEncoder(w).Encode(service.Object{"user": service.Object{"alias": alias}})
				case "/profile/specialization":
					if r.Method == http.MethodPost {
						posts++
						body, _ := io.ReadAll(r.Body)
						if r.Header.Get("X-CSRF-Token") != "SYNTHETIC_CSRF" || r.Header.Get("Referer") != service.Origin+"/profile/specialization" {
							t.Error("specialization write lost native CSRF/referer protection")
						}
						values, _ := url.ParseQuery(string(body))
						if values.Get("user[salary]") != "123" || strings.Join(values["user[user_skills_ids][]"], ",") != ",101,537" || strings.Join(values["user[raw_specialization_ids][]"], ",") != "2,4" || strings.Join(values["user[remote]"], ",") != "0,1" {
							t.Errorf("unrequested native metadata changed: %v", values)
						}
						ordered := "user%5Bforeign_languages%5D%5B%5D%5Blanguage_id%5D=71&user%5Bforeign_languages%5D%5B%5D%5Bgrade_id%5D=7&user%5Bforeign_languages%5D%5B%5D%5Blanguage_id%5D=1&user%5Bforeign_languages%5D%5B%5D%5Bgrade_id%5D=5"
						if !strings.HasSuffix(string(body), ordered) {
							t.Fatalf("Rails language/grade rows not interleaved: %s", body)
						}
						if scenario == "rate-limit" {
							w.Header().Set("Retry-After", "120")
							w.WriteHeader(429)
							return
						}
						headline = values.Get("user[resume_headline]")
						languages = []any{service.Object{"languageId": 71, "gradeId": 7, "title": "Русский родной язык"}, service.Object{"languageId": 1, "gradeId": 5, "title": "Английский С1"}}
						if scenario == "reordered-languages" {
							languages[0], languages[1] = languages[1], languages[0]
						}
						fmt.Fprint(w, `window.location = "/profile/specialization";`)
						return
					}
					writePage(w, service.Object{"allowSelectGroup": false, "categoryFieldName": "user[raw_specialization_ids][]", "skillsFieldName": "user[user_skills_ids][]", "selectedCategories": []any{2, 4}, "selectedSkills": []any{service.Object{"value": 101}, service.Object{"value": 537}}, "selectedLanguages": languages, "userCategoriesData": service.Object{}})
					salary := "123"
					if posts > 0 && scenario == "metadata-drift" {
						salary = "999"
					}
					fmt.Fprintf(w, `<a id="top_avatar" href="/%s">Owner</a><form action="/profile/specialization" method="post" data-remote="true"><input name="_method" value="patch"><input name="user[resume_headline]" value="%s"><input name="user[salary]" value="%s"><input type="hidden" name="user[remote]" value="0"><input type="checkbox" name="user[remote]" value="1" checked><input name="commit" type="submit" value="Save"></form>`, alias, html.EscapeString(headline), salary)
				case "/" + alias:
					rows := []any{}
					for _, value := range languages {
						row := value.(map[string]any)
						rows = append(rows, service.Object{"language_id": row["languageId"], "grade_id": row["gradeId"], "title": row["title"]})
					}
					writePage(w, service.Object{"user": service.Object{"id": alias, "title": "Synthetic Applicant", "isOwner": true, "shouldDisplayInfo": true, "print": service.Object{"html": service.Origin + "/" + alias + "/print"}, "specialization": service.Object{"title": headline}, "foreignLanguages": rows}, "resume": service.Object{"about": service.Object{"value": "Unchanged summary"}}})
				case "/" + alias + "/print":
					level := "Русский родной язык"
					if scenario == "stale-print" {
						level = "Русский C2"
					}
					if scenario == "extra-print-language" {
						level += " • Татарский родной язык"
					}
					fmt.Fprintf(w, `<div class="content"><h1 class="full_name">Synthetic Applicant</h1><a href="%s/%s">Profile</a><p>%s</p><h2>Знание языков</h2><p>%s • Английский C1</p></div>`, service.Origin, alias, html.EscapeString(headline), level)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			client := service.NewClient(httpClient)
			var patch service.Object
			_ = json.Unmarshal([]byte(`{"headline":"Exact LinkedIn headline & tools","languages":[{"language_id":71,"grade_id":7},{"language_id":1,"grade_id":5}]}`), &patch)
			result, err := client.UpdateSpecialization(t.Context(), patch, scenario != "preview", t.TempDir())
			if scenario == "preview" {
				if err != nil || posts != 0 || result["applied"] != false {
					t.Fatalf("preview wrote or failed: %v %v posts=%d", result, err, posts)
				}
				return
			}
			if scenario != "apply" && scenario != "reordered-languages" {
				if err == nil || posts != 1 {
					t.Fatalf("unsafe save succeeded or retried: %v %v posts=%d", result, err, posts)
				}
				return
			}
			if err != nil || posts != 1 || result["verified"] != true || headline != "Exact LinkedIn headline & tools" {
				t.Fatalf("save not independently verified: %v %v posts=%d", result, err, posts)
			}
			info, err := os.Stat(result["backup_path"].(string))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("private backup missing: %v %v", info, err)
			}
		})
	}
}
