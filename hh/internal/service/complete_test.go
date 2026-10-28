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
	"strings"
	"testing"
	"time"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

// Native wire contract captured with the browser POST aborted on 2026-10-28.
// This boundary test catches publishing the wrong draft, silently continuing an
// unsupported builder flow, accepting an unverified receipt or account mutation.
func TestCompleteCloneChecksNativeFlowAndAccountReadback(t *testing.T) {
	for _, scenario := range []string{"preview", "apply", "unsupported-screen", "wrong-receipt", "still-unfinished", "changed-peer", "changed-profile", "changed-profile-assignment", "missing-print-text", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			posts := 0
			resumes := map[string]hh.Object{}
			for _, hash := range hashes[:2] {
				resumes[hash] = hh.Object{
					"_attributes":      hh.Object{"hash": hash, "permission": "edit", "status": "not_finished", "hasErrors": false, "canPublishOrUpdate": true},
					"title":            []any{hh.Object{"string": "Developer tools engineer"}},
					"professionalRole": []any{hh.Object{"string": 96}},
					"skills":           []any{hh.Object{"string": "Shipped useful developer tools"}},
					"accessType":       []any{hh.Object{"string": "clients"}},
					"proftest":         []any{hh.Object{"attached": false, "resultsReceived": false, "visible": true}},
				}
			}
			profile := hh.Object{"email": "mail@example.com", "firstName": "Example"}
			profile["experience"] = []any{hh.Object{"id": 1, "resumes": []any{hh.Object{"hash": hashes[0]}, hh.Object{"hash": hashes[1]}}, "companyLogos": hh.Object{"logo": []any{hh.Object{"type": "ORIGINAL", "url": "/original.png"}, hh.Object{"type": "small", "url": "/small.png"}}}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/shards/applicant/resumes":
					_ = json.NewEncoder(w).Encode(hh.Object{"resumes": []any{hh.Object{"hash": hashes[1]}, hh.Object{"hash": hashes[0]}}})
				case "/applicant/resume":
					_ = json.NewEncoder(w).Encode(resumes[r.URL.Query().Get("resume")])
				case "/shards/applicant/profile/get_full_data":
					_ = json.NewEncoder(w).Encode(hh.Object{"profile": hh.Object{"fields": profile}})
				case "/profile/resume/professional_role":
					next := "professional_role"
					if scenario == "unsupported-screen" {
						next = "phone_confirmation"
					}
					builderResume := hh.Object{}
					for key, value := range resumes[r.URL.Query().Get("resume")] {
						if key != "proftest" {
							builderResume[key] = value
						}
					}
					state, _ := json.Marshal(hh.Object{"scheme": hh.Object{"resume": builderResume, "nextIncompleteScreenId": next, "screens": []any{hh.Object{"screenId": next}}}})
					fmt.Fprintf(w, `<template>%s</template><template id="HH-Lux-InitialState">{}</template>`, html.EscapeString(string(state)))
				case "/profile/shards/resume/update":
					posts++
					var payload hh.Object
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					resume, _ := payload["resume"].(map[string]any)
					wireProfile, _ := payload["profile"].(map[string]any)
					wireQuestions, _ := payload["questionToAnswerMap"].(map[string]any)
					properties, _ := payload["additionalProperties"].(map[string]any)
					fingerprints, _ := payload["userPrint"].(map[string]any)
					if r.Method != http.MethodPost || r.Header.Get("X-Xsrftoken") != "fixture-token" || len(payload) != 7 || payload["resumeHash"] != hashes[0] || payload["currentScreenId"] != "professional_role" || len(wireProfile) != 0 || len(wireQuestions) != 0 || properties["anyJob"] != false || fingerprints["fingerprintIteration2"] != "captured-one" {
						t.Errorf("completion violated recorded native contract: %v", payload)
					}
					if fmt.Sprint(resume["professionalRole"]) != "[96]" || fmt.Sprint(resume["title"]) != "[Developer tools engineer]" || len(resume) != 2 {
						t.Errorf("completion overwrote draft content: %v", resume)
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(http.StatusTooManyRequests)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					if scenario != "still-unfinished" {
						attrs := resumes[hashes[0]]["_attributes"].(map[string]any)
						attrs["status"], attrs["isSearchable"] = "on_moderation", true
					}
					if scenario == "changed-peer" {
						resumes[hashes[1]]["title"] = []any{hh.Object{"string": "Wrong change"}}
					}
					if scenario == "changed-profile" {
						profile["email"] = "changed@example.com"
					}
					profileExperience := profile["experience"].([]any)[0].(map[string]any)
					assignments := profileExperience["resumes"].([]any)
					assignments[0], assignments[1] = assignments[1], assignments[0]
					logos := profileExperience["companyLogos"].(map[string]any)["logo"].([]any)
					logos[0], logos[1] = logos[1], logos[0]
					if scenario == "changed-profile-assignment" {
						profileExperience["resumes"] = assignments[:1]
					}
					receiptID := hashes[0]
					if scenario == "wrong-receipt" {
						receiptID = hashes[1]
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"nextIncompleteScreenId": nil, "resume": hh.Object{"_attributes": hh.Object{"hash": receiptID}}})
				default:
					hash := strings.TrimPrefix(r.URL.Path, "/resume/")
					if r.Method != http.MethodGet || hash == r.URL.Path || r.URL.Query().Get("print") != "true" {
						t.Errorf("unexpected endpoint: %s %s", r.Method, r.URL)
						http.NotFound(w, r)
						return
					}
					state, _ := json.Marshal(hh.Object{"resume": hh.Object{"hash": hash, "title": hh.Object{"value": "Developer tools engineer"}, "skills": hh.Object{"value": "Shipped useful developer tools"}}})
					visible := "Developer tools engineer Shipped useful developer tools"
					if posts > 0 && scenario == "missing-print-text" {
						visible = "Developer tools engineer"
					}
					fmt.Fprintf(w, `<div id="HH-React-Root">%s</div><template id="HH-Lux-InitialState">%s</template>`, visible, html.EscapeString(string(state)))
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			native, _ := url.Parse("https://resume-profile-front.hh.ru")
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(native, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			transport := httpClient.Transport
			httpClient.Jar = jar
			httpClient.Transport = duplicateTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == native.Host {
					local := r.Clone(r.Context())
					local.URL.Scheme, local.URL.Host = base.Scheme, base.Host
					return transport.RoundTrip(local)
				}
				return transport.RoundTrip(r)
			})
			client := hh.NewClient(httpClient, base)
			id, _ := hh.NewResumeIDFromValue(hashes[0])
			metadata := hh.CloneMetadata{Origin: server.URL, UserAgent: "Captured browser fixture", CapturedAt: time.Now(), UserPrint: map[string]string{"fingerprintIteration2": "captured-one", "fingerprintSp": "captured-two"}}
			plan, err := client.Complete(context.Background(), id, metadata, scenario != "preview", t.TempDir())
			wantError := map[string]string{"unsupported-screen": "single profession-confirmation flow", "wrong-receipt": "no finished receipt", "still-unfinished": "still unfinished", "changed-peer": "ISOLATION FAILURE", "changed-profile": "shared profile fields changed", "changed-profile-assignment": "shared profile fields changed", "missing-print-text": "absent from employer print view", "rate-limit": "rate_limited"}[scenario]
			wantPosts := 1
			if scenario == "preview" || scenario == "unsupported-screen" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatalf("expected %d posts, got %d", wantPosts, posts)
			}
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("want %q, got %v", wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preview" {
				if plan.Applied || plan.Verified || plan.Status != "not_finished" {
					t.Fatalf("preview claims publication: %+v", plan)
				}
			} else if !plan.Applied || !plan.Verified || !plan.Searchable || plan.Status != "on_moderation" || len(plan.OtherResumesUnchanged) != 1 {
				t.Fatalf("missing completion readback: %+v", plan)
			}
		})
	}
}
