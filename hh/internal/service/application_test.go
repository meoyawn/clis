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

	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestApplicationApprovalIdentityAndReceipt(t *testing.T) {
	const letter = "Ксения, привет!\n\nAndroid offline-first: https://example.com/work?a=1&b=2"
	for _, scenario := range []string{"preview", "send", "unapproved", "tampered-letter", "changed-resume", "wrong-vacancy", "test-required", "already-applied", "wrong-readback", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			posts, requests := 0, 0
			submitted, changed := false, false
			responseStatus := func() hh.Object {
				resumes := hh.Object{}
				for i, hash := range hashes {
					id := fmt.Sprint(101 + i)
					resumes[id] = hh.Object{"hash": hash, "id": id, "isIncomplete": false}
				}
				used, unused, topics := []any{}, []any{"101", "102", "103"}, []any{}
				if submitted {
					id := "102"
					if scenario == "wrong-readback" {
						id = "101"
					}
					used, unused = []any{id}, []any{}
					topics = []any{hh.Object{"id": "98765", "resumeId": id}}
				}
				return hh.Object{"alreadyApplied": submitted || scenario == "already-applied", "responseImpossible": false,
					"by_country_applicant_visibility": hh.Object{"responseAllowed": true}, "test": hh.Object{"hasTests": scenario == "test-required"},
					"letterMaxLength": 10000, "shortVacancy": hh.Object{"vacancyId": 135582813, "company": hh.Object{"id": 125}, "@responseLetterRequired": true},
					"resumes": resumes, "unusedResumeIds": unused, "usedResumeIds": used, "resumeInconsistencies": hh.Object{}, "negotiations": hh.Object{"topicList": topics}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/vacancy/135582813":
					id := 135582813
					if scenario == "wrong-vacancy" {
						id++
					}
					state, _ := json.Marshal(hh.Object{"vacancyView": hh.Object{"vacancyFull": hh.Object{"vacancy": hh.Object{"vacancyId": id, "name": "Senior Android", "company": hh.Object{"id": 125, "name": "Employer"}, "description": "<p>Build offline-first Android applications</p>", "status": hh.Object{"archived": false}}}}, "applicantVacancyResponseStatuses": hh.Object{"135582813": responseStatus()}})
					fmt.Fprintf(w, `<template id="HH-Lux-InitialState">%s</template>`, html.EscapeString(string(state)))
				case "/applicant/vacancy_response/popup":
					if r.Method == http.MethodGet {
						if r.URL.Query().Get("vacancyId") != "135582813" {
							t.Errorf("wrong vacancy preflight: %s", r.URL)
						}
						_ = json.NewEncoder(w).Encode(hh.Object{"type": "modal", "responsePopup": hh.Object{"type": "normal", "vacancy": hh.Object{"vacancyId": 135582813}}, "responseStatus": responseStatus()})
						return
					}
					posts++
					if r.Header.Get("X-Xsrftoken") != "fixture-token" {
						t.Error("application lacks CSRF protection")
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Errorf("not the native multipart contract: %v", err)
					}
					if r.FormValue("vacancy_id") != "135582813" || r.FormValue("resume_hash") != hashes[1] || r.FormValue("letter") != letter {
						t.Errorf("wrong destination, resume or exact letter: %v", r.Form)
					}
					if r.FormValue("mark_applicant_visible_in_vacancy_country") != "false" || r.FormValue("country_ids") != "[]" {
						t.Error("unexpected country visibility expansion")
					}
					if scenario == "rate-limit" {
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					submitted = true
					fmt.Fprint(w, `{"topic_id":"98765"}`)
				case "/applicant/resume":
					if r.URL.Query().Get("resume") != hashes[1] {
						t.Errorf("default or wrong resume selected: %s", r.URL)
					}
					title := "Chosen Android resume"
					if changed {
						title = "Edited by human after review"
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"_attributes": hh.Object{"hash": hashes[1], "id": "102"}, "title": []any{hh.Object{"string": title}}})
				case "/resume/" + hashes[1]:
					fmt.Fprintf(w, `<div id="HH-React-Root"><p>Chosen Android resume</p><a href="mailto:mail@example.com">Email</a></div><template id="HH-Lux-InitialState">%s</template>`, html.EscapeString(fmt.Sprintf(`{"resume":{"hash":%q}}`, hashes[1])))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			jar, _ := cookiejar.New(nil)
			jar.SetCookies(base, []*http.Cookie{{Name: "_xsrf", Value: "fixture-token"}})
			httpClient := server.Client()
			httpClient.Jar = jar
			client := hh.NewClient(httpClient, base)
			vacancy, _ := hh.NewVacancyIDFromValue("https://kazan.hh.ru/vacancy/135582813?from=main")
			resume, _ := hh.NewResumeIDFromValue(hashes[1])
			draft, err := client.PrepareApplication(context.Background(), vacancy, resume, letter)
			if scenario == "wrong-vacancy" || scenario == "test-required" || scenario == "already-applied" {
				if err == nil || posts != 0 {
					t.Fatalf("unsupported or duplicate application accepted: %v posts=%d", err, posts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if posts != 0 || draft.ResumeID != hashes[1] || draft.Letter != letter || len(draft.ReviewSHA256) != 64 {
				t.Fatalf("preparation changed state or package: %+v posts=%d", draft, posts)
			}
			if scenario == "preview" {
				return
			}
			approved := draft.ReviewSHA256
			if scenario == "unapproved" {
				approved = ""
			}
			if scenario == "tampered-letter" {
				draft.Letter += " unapproved change"
			}
			changed = scenario == "changed-resume"
			priorRequests := requests
			result, err := client.SubmitApplication(context.Background(), *draft, approved)
			if scenario == "unapproved" || scenario == "tampered-letter" {
				if err == nil || posts != 0 || requests != priorRequests {
					t.Fatalf("approval failed to prevent network access: %v requests=%d posts=%d", err, requests-priorRequests, posts)
				}
				return
			}
			wantError := map[string]string{"changed-resume": "changed since approval", "wrong-readback": "does not confirm", "rate-limit": "rate_limited"}[scenario]
			if wantError != "" {
				wantPosts := 1
				if scenario == "changed-resume" {
					wantPosts = 0
				}
				if err == nil || !strings.Contains(err.Error(), wantError) || posts != wantPosts {
					t.Fatalf("want %q, %d posts; got %v, %d posts", wantError, wantPosts, err, posts)
				}
				return
			}
			if err != nil || result["verified"] != true || result["submitted"] != true || posts != 1 {
				t.Fatalf("application receipt not verified: result=%v err=%v posts=%d", result, err, posts)
			}
		})
	}
}
