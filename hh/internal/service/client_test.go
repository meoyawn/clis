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
	"reflect"
	"strings"
	"testing"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

// These reduced fixtures follow the two distinct native wire contracts:
// applicant API arrays vs print-page {value: ...} wrappers. They deliberately
// have three resumes, share an experience ID, and use unrelated numeric IDs.
var hashes = []string{strings.Repeat("a", 38), strings.Repeat("b", 38), strings.Repeat("c", 38)}

func TestResumeUpdateIsolationAndPrintVerification(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "hide-contacts", "decorative-metadata", "fork-shared-experience", "hidden-print-experience", "cross-resume-write", "stale-print", "hidden-print-text", "wrong-print-id", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			resumes := make(map[string]hh.Object)
			for i, hash := range hashes {
				resumes[hash] = hh.Object{
					"_attributes": hh.Object{"hash": hash, "id": fmt.Sprint(100 + i), "permission": "edit"},
					"title":       []any{hh.Object{"string": fmt.Sprintf("Engineer %d", i)}},
					"skills":      []any{hh.Object{"string": fmt.Sprintf("Before %d", i)}},
					"keySkills":   []any{hh.Object{"string": "Go"}},
					"experience":  []any{hh.Object{"id": float64(42), "companyName": "Shared historical employer", "position": "Engineer", "startDate": "2020-01-01", "endDate": nil, "description": fmt.Sprintf("History %d", i)}},
				}
			}
			posts := 0
			reads := 0
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.Method+" "+r.URL.RequestURI())
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/applicant/resume":
					reads++
					if scenario == "decorative-metadata" {
						row := resumes[r.URL.Query().Get("resume")]["experience"].([]any)[0].(map[string]any)
						if reads%2 == 0 {
							row["companyLogos"] = []any{"small.svg", "big.svg"}
						} else {
							row["companyLogos"] = []any{"big.svg", "small.svg"}
						}
					}
					_ = json.NewEncoder(w).Encode(resumes[r.URL.Query().Get("resume")])
				case "/shards/applicant/resumes":
					var items []any
					for _, hash := range hashes {
						items = append(items, hh.Object{"_attributes": hh.Object{"hash": hash, "id": "different-numeric-id"}})
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"resumes": items})
				case "/applicant/resume/edit":
					posts++
					if r.Method != http.MethodPost || r.URL.Query().Get("resume") != hashes[1] {
						t.Errorf("wrong mutation target: %s %s", r.Method, r.URL)
					}
					if r.Header.Get("X-Xsrftoken") != "fixture-token" {
						t.Error("missing CSRF protection")
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(429)
						fmt.Fprint(w, `{"code":"rate_limited","token":"must-be-redacted"}`)
						return
					}
					var patch hh.Object
					if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
						t.Fatal(err)
					}
					if len(patch) != 1 {
						t.Errorf("unrequested fields sent: %v", patch)
					}
					if scenario == "hide-contacts" {
						resumes[hashes[1]]["hiddenFields"] = []any{hh.Object{"string": "phones"}, hh.Object{"string": "other_contacts"}}
					} else if scenario == "fork-shared-experience" || scenario == "hidden-print-experience" {
						row := patch["experience"].([]any)[0].(map[string]any)
						if row["id"] != float64(42) {
							t.Errorf("lost original experience ID: %v", row["id"])
						}
						row["id"] = float64(4242)
						resumes[hashes[1]]["experience"] = []any{row}
					} else {
						values := patch["skills"].([]any)
						resumes[hashes[1]]["skills"] = []any{hh.Object{"string": values[0]}}
					}
					if scenario == "cross-resume-write" {
						resumes[hashes[0]]["skills"] = []any{hh.Object{"string": "Unexpected shared change"}}
					}
					fmt.Fprint(w, `{"errors":{},"data":{}}`)
				default:
					if !strings.HasPrefix(r.URL.Path, "/resume/") || r.URL.Query().Get("print") != "true" {
						http.NotFound(w, r)
						return
					}
					hash := strings.TrimPrefix(r.URL.Path, "/resume/")
					about := resumes[hash]["skills"].([]any)[0].(map[string]any)["string"].(string)
					if scenario == "stale-print" {
						about = "Before 1"
					}
					if scenario == "wrong-print-id" {
						hash = hashes[0]
					}
					printResume := hh.Object{"hash": hash, "skills": hh.Object{"value": about}, "experience": hh.Object{"value": resumes[hash]["experience"]}}
					if scenario == "hide-contacts" {
						// HH prints hiddenFields as a direct scalar array, without the
						// {value: ...} wrapper used by most resume fields.
						printResume["hiddenFields"] = []any{"phones", "other_contacts"}
					}
					state, _ := json.Marshal(hh.Object{"resume": printResume})
					visible := about
					if scenario == "hidden-print-text" {
						visible = "Stale HTML despite current embedded JSON"
					}
					w.Header().Set("Content-Type", "text/html")
					var experienceText strings.Builder
					if scenario != "hidden-print-experience" {
						for _, row := range resumes[hash]["experience"].([]any) {
							item := row.(map[string]any)
							fmt.Fprintf(&experienceText, "<div>%s</div><div>%s</div><p>%s</p>", html.EscapeString(item["companyName"].(string)), html.EscapeString(item["position"].(string)), html.EscapeString(item["description"].(string)))
						}
					}
					fmt.Fprintf(w, `<div id="HH-React-Root"><h2>About me</h2><p>%s</p>%s</div><template id="HH-Lux-InitialState">%s</template>`, html.EscapeString(visible), experienceText.String(), html.EscapeString(string(state)))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(base, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			httpClient.Jar = jar
			client := hh.NewClient(httpClient, base)
			id, _ := hh.NewResumeIDFromValue(hashes[1])
			backupDir := t.TempDir()
			patch := hh.Object{"skills": "New independently rendered <Go> & TypeScript"}
			if scenario == "hide-contacts" {
				patch = hh.Object{"hiddenFields": []any{"phones", "other_contacts"}}
			}
			if scenario == "fork-shared-experience" || scenario == "hidden-print-experience" {
				patch = hh.Object{"experience": []any{hh.Object{"id": float64(42), "companyName": "Shared historical employer", "position": "Engineer", "startDate": "2020-01-01", "endDate": nil, "description": "New history only in selected resume"}}}
			}
			plan, err := client.Update(context.Background(), id, patch, scenario != "preview", backupDir)
			wantError := map[string]string{"cross-resume-write": "ISOLATION FAILURE", "stale-print": "print view differs", "hidden-print-text": "absent from employer print view", "hidden-print-experience": "absent from employer print view", "wrong-print-id": "identity mismatch", "rate-limit": "rate_limited"}[scenario]
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("want %q, got %v", wantError, err)
				}
				if strings.Contains(err.Error(), "must-be-redacted") {
					t.Fatal("token leaked in diagnostic")
				}
				if posts != 1 {
					t.Fatalf("write retried: %d posts", posts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview" {
				if posts != 0 || plan.Applied || plan.Verified {
					t.Fatalf("preview mutated state: posts=%d plan=%+v", posts, plan)
				}
				return
			}
			if posts != 1 || !plan.Applied || !plan.Verified {
				t.Fatalf("update not fully verified: posts=%d plan=%+v", posts, plan)
			}
			if !reflect.DeepEqual(plan.OtherResumesUnchanged, []string{hashes[0], hashes[2]}) {
				t.Fatalf("N-resume isolation not checked: %v", plan.OtherResumesUnchanged)
			}
			if scenario != "fork-shared-experience" && scenario != "hide-contacts" && !strings.Contains(plan.PrintText, "<Go> & TypeScript") {
				t.Fatal("print output did not preserve decoded text")
			}
			if scenario == "fork-shared-experience" {
				for _, hash := range []string{hashes[0], hashes[2]} {
					row := resumes[hash]["experience"].([]any)[0].(map[string]any)
					if row["id"] != float64(42) || row["description"] == "New history only in selected resume" {
						t.Fatalf("shared experience changed in %s: %v", hash, row)
					}
				}
			}
			stat, err := os.Stat(plan.BackupPath)
			if err != nil {
				t.Fatal(err)
			}
			if stat.Mode().Perm() != 0600 {
				t.Fatalf("backup permissions: %v", stat.Mode().Perm())
			}
			data, _ := os.ReadFile(plan.BackupPath)
			if !strings.Contains(string(data), "Before 1") || !strings.Contains(string(data), hashes[2]) {
				t.Fatal("backup does not preserve selected and other resumes")
			}
			for _, path := range paths {
				if strings.HasPrefix(path, "POST /profile/") {
					t.Fatalf("used shared profile endpoint: %s", path)
				}
			}
		})
	}
}

func TestResumeIdentityUsesPublicHashAndRejectsWrongPage(t *testing.T) {
	for _, value := range []string{hashes[0], "https://kazan.hh.ru/resume/" + hashes[0] + "?print=true", "https://hh.ru/resume/edit/" + hashes[0] + "/about"} {
		id, err := hh.NewResumeIDFromValue(value)
		if err != nil || id.String() != hashes[0] {
			t.Fatalf("valid identity %q: %v %s", value, err, id)
		}
	}
	for _, value := range []string{"63085537", "latest", "https://hh.ru.evil.test/resume/" + hashes[0], "https://user:secret@hh.ru/resume/" + hashes[0], "https://hh.ru/search/resume"} {
		if _, err := hh.NewResumeIDFromValue(value); err == nil {
			t.Fatalf("accepted unsafe identity %q", value)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"_attributes":{"hash":%q,"permission":"edit"}}`, hashes[1])
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := hh.NewClient(server.Client(), base)
	id, _ := hh.NewResumeIDFromValue(hashes[0])
	if _, err := client.Get(context.Background(), id); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("wrong resume accepted: %v", err)
	}
}

func TestInvalidPatchNeverMakesARequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := hh.NewClient(server.Client(), base)
	id, _ := hh.NewResumeIDFromValue(hashes[0])
	for _, patch := range []hh.Object{{"resume": "another-id"}, {"skills": nil}, {"skills": []any{float64(7)}}, {"experience": []any{hh.Object{"companyName": "Missing required data"}}}} {
		if _, err := client.Update(context.Background(), id, patch, true, t.TempDir()); err == nil {
			t.Fatalf("invalid patch accepted: %v", patch)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid patch made %d network requests", requests)
	}
}

func TestPrintViewKeepsReadableLinkBoundaries(t *testing.T) {
	text, err := hh.PrintText([]byte(`<div id="HH-React-Root"><div><span>Казань</span><a href="https://example.com">example.com</a></div><p>Go &amp; TypeScript</p><script>private-script-token</script></div><template id="HH-Lux-InitialState">hidden-state</template>`))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Казань example.com (https://example.com)\nGo & TypeScript" {
		t.Fatalf("unreadable or non-visible print content: %q", text)
	}
}

func TestViewReportsActualContactLinksAndPhoneShortcuts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := fmt.Sprintf(`{"resume":{"hash":%q,"otherCommunicationMethods":{"value":[{"description":"GitHub","value":"https://github.com/example"}]}}}`, hashes[0])
		fmt.Fprintf(w, `<div id="HH-React-Root"><h2>Contacts</h2><a href="mailto:mail@example.com">mail@example.com</a><a data-qa="resume-phone-deep-link-telegram">Telegram</a><a data-qa="resume-personalsite-personal" href="https://old.example.com">Old site</a><a data-qa="resume-personalsite-personal" href="https://github.com/example">GitHub</a></div><template id="HH-Lux-InitialState">%s</template>`, html.EscapeString(state))
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	id, _ := hh.NewResumeIDFromValue(hashes[0])
	view, err := hh.NewClient(server.Client(), base).View(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	var output hh.Object
	_ = json.Unmarshal(encoded, &output)
	contacts, ok := output["contacts"].(map[string]any)
	if !ok {
		t.Fatal("view omitted contact evidence; stale links can pass unnoticed")
	}
	websites := contacts["websites"].([]any)
	if len(websites) != 2 || websites[0].(map[string]any)["value"] != "https://old.example.com" {
		t.Fatalf("actual rendered old contact was lost: %v", websites)
	}
	if !reflect.DeepEqual(contacts["generated_phone_shortcuts"], []any{"Telegram"}) {
		t.Fatalf("phone shortcut misreported as a configured account: %v", contacts)
	}
}

func TestSharedContactsUpdateChecksAllResumeViews(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "decorative-order", "extra-rendered-link", "changed-resume", "changed-messenger", "changed-profile", "rejected", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			profile := hh.Object{"profile": hh.Object{"fields": hh.Object{
				"otherCommunicationMethods": []any{hh.Object{"value": "https://old.example.com", "description": "Old site", "links": hh.Object{"web": "https://old.example.com"}}},
				"communicationMethods":      []any{hh.Object{"telegram": nil}},
				"firstName":                 []any{hh.Object{"string": "Human"}},
			}}}
			posts := 0
			profileReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/shards/applicant/profile/get_full_data":
					profileReads++
					if scenario == "decorative-order" {
						a, b := hashes[0], hashes[2]
						if profileReads%2 == 0 {
							a, b = b, a
						}
						fields := profile["profile"].(map[string]any)["fields"].(map[string]any)
						fields["primaryEducation"] = []any{hh.Object{"name": "Unchanged university", "resumes": []any{hh.Object{"hash": a}, hh.Object{"hash": b}}}}
						fields["experience"] = []any{hh.Object{"description": "Unchanged work", "companyLogos": []any{a, b}}}
					}
					_ = json.NewEncoder(w).Encode(profile)
				case "/shards/applicant/profile/update":
					posts++
					if r.Method != "POST" || r.Header.Get("X-Xsrftoken") != "fixture-token" || r.URL.RawQuery != "" {
						t.Errorf("wrong shared profile request: %s %s", r.Method, r.URL)
					}
					if scenario == "rate-limit" {
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					if scenario == "rejected" {
						fmt.Fprint(w, `{"errors":[{"field":"otherCommunicationMethods","code":"INVALID"}]}`)
						return
					}
					var body hh.Object
					_ = json.NewDecoder(r.Body).Decode(&body)
					patch := body["profile"].(map[string]any)
					if len(body) != 1 || len(patch) != 1 {
						t.Errorf("shared write sent unrelated fields: %v", body)
					}
					profile["profile"].(map[string]any)["fields"].(map[string]any)["otherCommunicationMethods"] = patch["otherCommunicationMethods"]
					if scenario == "changed-profile" {
						profile["profile"].(map[string]any)["fields"].(map[string]any)["firstName"] = []any{hh.Object{"string": "Wrong human"}}
					}
					_ = json.NewEncoder(w).Encode(profile)
				case "/shards/applicant/resumes":
					items := []any{}
					for _, hash := range hashes {
						items = append(items, hh.Object{"_attributes": hh.Object{"hash": hash}})
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"resumes": items})
				case "/applicant/resume":
					title := "Unchanged engineer"
					if posts > 0 && scenario == "changed-resume" && r.URL.Query().Get("resume") == hashes[2] {
						title = "Unexpected mutation"
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"_attributes": hh.Object{"hash": r.URL.Query().Get("resume")}, "title": []any{hh.Object{"string": title}}})
				default:
					if !strings.HasPrefix(r.URL.Path, "/resume/") {
						http.NotFound(w, r)
						return
					}
					hash := strings.TrimPrefix(r.URL.Path, "/resume/")
					state := fmt.Sprintf(`{"resume":{"hash":%q}}`, hash)
					var links strings.Builder
					rows := profile["profile"].(map[string]any)["fields"].(map[string]any)["otherCommunicationMethods"].([]any)
					for _, row := range rows {
						x := row.(map[string]any)
						fmt.Fprintf(&links, `<a data-qa="resume-personalsite-personal" href="%s">%s</a>`, html.EscapeString(x["value"].(string)), html.EscapeString(x["description"].(string)))
					}
					if posts > 0 && scenario == "extra-rendered-link" && hash == hashes[2] {
						links.WriteString(`<a data-qa="resume-personalsite-personal" href="https://old.example.com">Old site</a>`)
					}
					if posts > 0 && scenario == "changed-messenger" && hash == hashes[2] {
						links.WriteString(`<a data-qa="resume-communication-method-telegram" href="https://t.me/wrong">Telegram</a>`)
					}
					fmt.Fprintf(w, `<div id="HH-React-Root"><a href="tel:+100000000">Phone</a><a href="mailto:mail@example.com">Email</a><a data-qa="resume-phone-deep-link-telegram">Telegram</a>%s</div><template id="HH-Lux-InitialState">%s</template>`, links.String(), html.EscapeString(state))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(base, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			httpClient.Jar = jar
			client := hh.NewClient(httpClient, base)
			plan, err := client.UpdateContacts(context.Background(), hh.Object{"otherCommunicationMethods": []any{hh.Object{"value": "https://github.com/example", "description": "GitHub"}}}, scenario != "preview", t.TempDir())
			wantError := map[string]string{"extra-rendered-link": "extra, or mislabeled", "changed-resume": "unrequested resume content", "changed-messenger": "unrequested rendered", "changed-profile": "unrequested shared profile", "rejected": "INVALID", "rate-limit": "rate_limited"}[scenario]
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) || posts != 1 {
					t.Fatalf("want %q with one write; got error=%v posts=%d", wantError, err, posts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.AffectedResumes) != 3 {
				t.Fatalf("not all resumes checked: %+v", plan)
			}
			if scenario == "preview" {
				if posts != 0 || plan.Verified || plan.Applied {
					t.Fatalf("preview mutated shared profile: %+v", plan)
				}
				return
			}
			if posts != 1 || !plan.Verified || !plan.Applied || !plan.ResumeContentUnchanged {
				t.Fatalf("shared change lacks verification: %+v", plan)
			}
			info, err := os.Stat(plan.BackupPath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("shared backup must be private: %v %v", info, err)
			}
		})
	}
}
