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

func TestQuestionnaireReviewAndNativeSubmission(t *testing.T) {
	for _, scenario := range []string{"send", "changed-question", "tampered-answer", "choice-question", "extra-hidden", "unknown-answer"} {
		t.Run(scenario, func(t *testing.T) {
			posts, requests, nonce := 0, 0, 0
			submitted, changed := false, false
			status := func() hh.Object {
				used, unused, topics := []any{}, []any{"102"}, []any{}
				if submitted {
					used, unused = []any{"102"}, []any{}
					topics = []any{hh.Object{"id": "98765", "resumeId": "102"}}
				}
				return hh.Object{"alreadyApplied": submitted, "responseImpossible": false,
					"by_country_applicant_visibility": hh.Object{"responseAllowed": true},
					"test":                            hh.Object{"hasTests": true, "required": true, "testId": 700}, "letterMaxLength": 10000,
					"shortVacancy":    hh.Object{"vacancyId": 135582813, "company": hh.Object{"id": 125}},
					"resumes":         hh.Object{"102": hh.Object{"hash": hashes[1], "isIncomplete": false}},
					"unusedResumeIds": unused, "usedResumeIds": used, "negotiations": hh.Object{"topicList": topics}}
			}
			page := func(w http.ResponseWriter, state hh.Object, form string) {
				data, _ := json.Marshal(state)
				fmt.Fprintf(w, `%s<template id="HH-Lux-InitialState">%s</template>`, form, html.EscapeString(string(data)))
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch r.URL.Path {
				case "/vacancy/135582813":
					page(w, hh.Object{"vacancyView": hh.Object{"vacancyFull": hh.Object{"vacancy": hh.Object{
						"vacancyId": 135582813, "name": "Android engineer", "company": hh.Object{"id": 125, "name": "Synthetic Employer"},
						"description": "<p>Build useful applications</p>", "status": hh.Object{"archived": false}}}},
						"applicantVacancyResponseStatuses": hh.Object{"135582813": status()}}, "")
				case "/applicant/vacancy_response":
					if r.Method != http.MethodGet || r.URL.Query().Get("vacancyId") != "135582813" || r.URL.Query().Get("startedWithQuestion") != "false" {
						t.Errorf("unexpected questionnaire request: %s %s", r.Method, r.URL)
					}
					nonce++
					prompt := "Confirm the external form was sent"
					if changed {
						prompt = "A different question added by the employer"
					}
					choices := []any{}
					if scenario == "choice-question" {
						choices = []any{hh.Object{"id": "701", "text": "Choice"}}
					}
					form := fmt.Sprintf(`<form name="vacancy_response" method="POST"><input type="hidden" name="_xsrf" value="fixture-token"><input type="hidden" name="uidPk" value="700"><input type="hidden" name="guid" value="synthetic-guid-%d"><input type="hidden" name="startTime" value="%d"><input type="hidden" name="testRequired" value="true">`, nonce, nonce)
					if scenario == "extra-hidden" {
						form += `<input type="hidden" name="unexpected_challenge" value="unknown">`
					}
					form += `</form>`
					page(w, hh.Object{"applicantVacancyResponseStatuses": hh.Object{"135582813": status()},
						"vacancyTests": hh.Object{"135582813": hh.Object{"name": "Synthetic questionnaire", "required": "true", "uidPk": "700",
							"guid": fmt.Sprintf("synthetic-guid-%d", nonce), "startTime": fmt.Sprint(nonce), "description": "<p>Answer the question</p>",
							"tasks": []any{hh.Object{"id": 701, "description": prompt, "open": "true", "candidateSolutions": choices}}}}}, form)
				case "/applicant/vacancy_response/popup":
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet {
						fmt.Fprint(w, `{"type":"test-required"}`)
						return
					}
					posts++
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					for key, want := range map[string]string{"vacancy_id": "135582813", "resume_hash": hashes[1], "letter": "Reviewed letter",
						"task_701_text": "The form was sent.", "uidPk": "700", "guid": fmt.Sprintf("synthetic-guid-%d", nonce),
						"startTime": fmt.Sprint(nonce), "testRequired": "true", "withoutTest": "no", "_xsrf": "fixture-token",
						"mark_applicant_visible_in_vacancy_country": "false", "country_ids": "[]"} {
						if got := r.FormValue(key); got != want {
							t.Errorf("native field %s: got %q, want %q", key, got, want)
						}
					}
					if nonce != 2 || r.Header.Get("X-Xsrftoken") != "fixture-token" {
						t.Error("submission did not use a fresh form and CSRF protection")
					}
					submitted = true
					fmt.Fprint(w, `{"topic_id":"98765"}`)
				case "/applicant/resume":
					_ = json.NewEncoder(w).Encode(hh.Object{"_attributes": hh.Object{"hash": hashes[1], "id": "102"}, "title": []any{hh.Object{"string": "Chosen resume"}}})
				case "/resume/" + hashes[1]:
					page(w, hh.Object{"resume": hh.Object{"hash": hashes[1]}}, `<div id="HH-React-Root">Chosen resume</div>`)
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
			vacancy, _ := hh.NewVacancyIDFromValue("135582813")
			resume, _ := hh.NewResumeIDFromValue(hashes[1])
			answers := map[string]string{"701": "The form was sent."}
			if scenario == "unknown-answer" {
				answers = map[string]string{"702": "Unreviewed question"}
			}
			draft, err := client.PrepareApplication(context.Background(), vacancy, resume, "Reviewed letter", answers)
			if scenario == "choice-question" || scenario == "extra-hidden" || scenario == "unknown-answer" {
				if err == nil || posts != 0 {
					t.Fatalf("unsupported questionnaire accepted: %v posts=%d", err, posts)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(draft)
			if strings.Contains(string(data), "synthetic-guid") || strings.Contains(string(data), "fixture-token") {
				t.Fatal("exported review package contains private form fields")
			}
			// Exercise the saved-file boundary: ephemeral fields cannot survive it.
			var saved hh.ApplicationDraft
			if err := json.Unmarshal(data, &saved); err != nil {
				t.Fatal(err)
			}
			changed = scenario == "changed-question"
			if scenario == "tampered-answer" {
				saved.Questionnaire.Questions[0].Answer = "Unapproved change"
			}
			before := requests
			result, err := client.SubmitApplication(context.Background(), saved, draft.ReviewSHA256)
			if scenario == "tampered-answer" {
				if err == nil || requests != before || posts != 0 {
					t.Fatalf("changed answer bypassed approval: err=%v posts=%d", err, posts)
				}
				return
			}
			if scenario == "changed-question" {
				if err == nil || !strings.Contains(err.Error(), "changed since approval") || posts != 0 {
					t.Fatalf("changed prompt bypassed approval: err=%v posts=%d", err, posts)
				}
				return
			}
			if err != nil || result["verified"] != true || posts != 1 {
				t.Fatalf("questionnaire application not verified: result=%v err=%v posts=%d", result, err, posts)
			}
		})
	}
}
