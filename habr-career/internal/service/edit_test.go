package service_test

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"github.com/meoyawn/clis/habr-career/internal/service"
)

func TestExperienceDescriptionNativeFormPreservationAndReadback(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "wrong-owner", "unknown-field", "wrong-form", "missing-date-state", "missing-skills-state", "metadata-drift", "other-job-drift", "missing-print", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			const alias = "synthetic-user"
			const newDescription = "<p>Built end-to-end tests.</p><p>Implemented system-wide observability.</p>\n"
			description, posts := "<p>Previous description</p>", 0
			originalMetadata := url.Values{
				"_method": {"patch"}, "experience[company_name]": {"Synthetic Product"}, "experience[position]": {"Engineer"},
				"experience[specialization_id]": {"4", "4"}, "experience[qualification_id]": {"5"}, "presentTime": {"on"},
				"experience[experience_skills_ids][]": {"", "101", "537"},
				"experience[start_date(1i)]":          {"2020"}, "experience[start_date(2i)]": {"10"}, "experience[start_date(3i)]": {"1"},
				"experience[end_date(1i)]": {""}, "experience[end_date(2i)]": {""}, "experience[end_date(3i)]": {"1"},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/frontend_v1/users/me":
					owner := alias
					if scenario == "wrong-owner" {
						owner = "another-user"
					}
					_ = json.NewEncoder(w).Encode(service.Object{"user": service.Object{"alias": owner}})
				case "/profile/experiences/123/edit":
					state := service.Object{
						"fieldName": "experience", "startDate": "2020-10-01", "endDate": nil, "isPresentTime": true,
						"skillsFieldName": "experience[experience_skills_ids][]", "selectedSkills": []any{service.Object{"value": 101}, service.Object{"value": 537}},
						"categoryFieldName": "experience[specialization_id]", "selectedCategories": []any{4},
					}
					if scenario == "missing-date-state" {
						delete(state, "startDate")
					}
					if scenario == "missing-skills-state" {
						delete(state, "selectedSkills")
					}
					writePage(w, state)
					formID := "edit_experience_123"
					if scenario == "wrong-form" {
						formID = "edit_experience_456"
					}
					fmt.Fprintf(w, `<a id="top_avatar" href="/%s">Owner</a><form id="%s" action="/profile/experiences/123" method="post" data-remote="true">`, alias, formID)
					for key, values := range originalMetadata {
						if key == "experience[qualification_id]" || key == "presentTime" || key == "experience[experience_skills_ids][]" || strings.Contains(key, "_date(") {
							continue
						}
						if key == "experience[specialization_id]" {
							values = values[:1]
						}
						for _, value := range values {
							fmt.Fprintf(w, `<input type="hidden" name="%s" value="%s">`, html.EscapeString(key), html.EscapeString(value))
						}
					}
					qualification := "5"
					if posts > 0 && scenario == "metadata-drift" {
						qualification = "6"
					}
					fmt.Fprintf(w, `<select name="experience[qualification_id]"><option value="1">Junior</option><option value="%s" selected>Senior</option></select><input type="radio" name="experience[specialization_id]" value="UNSELECTED"><input name="unrelated" disabled><textarea name="experience[description]">%s</textarea>`, qualification, html.EscapeString(description))
					if scenario == "unknown-field" {
						fmt.Fprint(w, `<input name="experience[visibility]" value="public">`)
					}
					fmt.Fprint(w, `<input name="commit" type="submit" value="Save"></form>`)
				case "/profile/experiences/123":
					posts++
					if r.Method != http.MethodPost || r.Header.Get("X-CSRF-Token") != "SYNTHETIC_CSRF" || r.Header.Get("X-Requested-With") != "XMLHttpRequest" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") || !strings.Contains(r.Header.Get("Accept"), "text/javascript") {
						t.Error("profile update did not use the captured native AJAX contract")
					}
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.PostForm.Get("experience[description]") != newDescription {
						t.Error("description bytes changed in transport")
					}
					delete(r.PostForm, "experience[description]")
					if !reflect.DeepEqual(r.PostForm, originalMetadata) {
						t.Errorf("native repeated fields or unrelated values changed: %v", r.PostForm)
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "120")
						w.WriteHeader(429)
						return
					}
					description = strings.TrimRight(newDescription, "\r\n")
					fmt.Fprint(w, `window.location = "/profile/experiences";`)
				case "/" + alias:
					other := "Unchanged previous job"
					if posts > 0 && scenario == "other-job-drift" {
						other = "Unintended edit"
					}
					writePage(w, service.Object{"placeholder": nil, "user": service.Object{"id": alias, "title": "Synthetic Applicant", "isOwner": true, "shouldDisplayInfo": true, "print": service.Object{"html": service.Origin + "/" + alias + "/print"}}, "resume": service.Object{"about": service.Object{"value": "Unchanged summary"}, "companies": service.Object{"items": []any{
						service.Object{"title": "Synthetic Product", "positions": []any{service.Object{"title": "Engineer", "duration": "2020–present", "message": description}}},
						service.Object{"title": "Previous Employer", "positions": []any{service.Object{"title": "Engineer", "message": other}}},
					}}}})
				case "/" + alias + "/print":
					content := description
					if posts > 0 && scenario == "missing-print" {
						content = "Previous description only"
					}
					fmt.Fprintf(w, `<div class="content"><h1 class="full_name">Synthetic Applicant</h1><a href="%s/%s">Profile</a>%s</div>`, service.Origin, alias, content)
				default:
					t.Errorf("unexpected profile request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			client := service.NewClient(httpClient)
			resume, _ := domain.NewResumeIDFromValue(alias)
			experience, _ := domain.NewExperienceIDFromValue("123")
			dir := t.TempDir()
			result, err := client.UpdateExperience(t.Context(), resume, experience, newDescription, scenario != "preview", dir)
			if scenario == "preview" {
				if err != nil || posts != 0 || result["after"] != newDescription || result["applied"] != false {
					t.Fatalf("preview wrote or changed the package: %v %v", result, err)
				}
				return
			}
			if scenario == "apply" {
				if err != nil || posts != 1 || result["verified"] != true || result["applied"] != true {
					t.Fatalf("profile update not verified: %v %v posts=%d", result, err, posts)
				}
				info, err := os.Stat(result["backup_path"].(string))
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("private backup missing/unsafe: %v %v", info, err)
				}
				b, _ := os.ReadFile(result["backup_path"].(string))
				if strings.Contains(string(b), "SYNTHETIC_CSRF") || !strings.Contains(string(b), "Previous description") {
					t.Fatal("backup leaked CSRF or failed to preserve previous content")
				}
				return
			}
			if err == nil || result != nil {
				t.Fatalf("unsafe or unverified profile update reported success: %v %v", result, err)
			}
			wantPosts := 1
			if scenario == "wrong-owner" || scenario == "unknown-field" || scenario == "wrong-form" || scenario == "missing-date-state" || scenario == "missing-skills-state" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatalf("guard failed or update retried: %v posts=%d", err, posts)
			}
		})
	}
}

func TestUpdateExistingApplicationNeverCreatesAnotherResponse(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "not-editable", "wrong-author", "wrong-readback", "uncertain"} {
		t.Run(scenario, func(t *testing.T) {
			letter, patches := "Existing letter", 0
			const replacement = "New letter\nhttps://example.test/work\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/vacancies/1000000000" {
					author := "synthetic-user"
					if scenario == "wrong-author" {
						author = "another-user"
					}
					message := letter
					if patches > 0 && scenario == "wrong-readback" {
						message = "Unexpected different text"
					}
					writePage(w, service.Object{"userLogin": "synthetic-user", "visitor": service.Object{"login": "synthetic-user"}, "vacancy": service.Object{"id": 1000000000, "href": "/vacancies/1000000000", "title": "Synthetic Role", "description": "<p>Build tools</p>", "archived": false, "hidden": false, "company": service.Object{"id": 77, "title": "Synthetic Employer"}}, "createResponse": service.Object{"vacancyId": 1000000000, "response": service.Object{"id": 8101, "author": service.Object{"id": author}, "message": message, "canUpdate": scenario != "not-editable"}}})
					return
				}
				if r.Method != http.MethodPatch || r.URL.Path != "/api/frontend/vacancies/1000000000/responses/8101" {
					t.Errorf("letter edit attempted creation or a different destination: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				patches++
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
				}
				if len(r.MultipartForm.Value) != 1 || r.FormValue("body") != replacement || r.Header.Get("X-CSRF-Token") != "SYNTHETIC_CSRF" {
					t.Error("letter edit changed salary/fields/text or omitted CSRF protection")
				}
				letter = strings.TrimRight(r.FormValue("body"), "\r\n")
				if scenario == "uncertain" {
					w.WriteHeader(500)
					return
				}
				fmt.Fprint(w, `{}`)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			client := service.NewClient(httpClient)
			vacancy, _ := domain.NewVacancyIDFromValue("1000000000")
			resume, _ := domain.NewResumeIDFromValue("synthetic-user")
			dir := t.TempDir()
			result, err := client.UpdateApplication(t.Context(), vacancy, resume, replacement, scenario != "preview", dir)
			if scenario == "preview" || scenario == "apply" {
				wantPatches := 0
				if scenario == "apply" {
					wantPatches = 1
				}
				if err != nil || patches != wantPatches || result["response_id"] != "8101" || result["previous_letter"] != "Existing letter" || scenario == "apply" && result["verified"] != true {
					t.Fatalf("existing-response update = %v %v patches=%d", result, err, patches)
				}
				if scenario == "apply" {
					backups, _ := filepath.Glob(filepath.Join(dir, "*.json"))
					if len(backups) != 1 {
						t.Fatal("letter revision did not preserve its old version")
					}
				}
				return
			}
			if err == nil || result != nil || patches > 1 {
				t.Fatalf("unsafe/uncertain letter edit reported success or retried: %v %v patches=%d", result, err, patches)
			}
			if (scenario == "not-editable" || scenario == "wrong-author") && patches != 0 {
				t.Fatal("uneditable or foreign response was changed")
			}
			if scenario == "uncertain" {
				readback, err := client.Application(t.Context(), vacancy, resume)
				if err != nil || readback["letter"] != strings.TrimRight(replacement, "\r\n") || patches != 1 {
					t.Fatal("uncertain update cannot be inspected without another write")
				}
			}
		})
	}
}
