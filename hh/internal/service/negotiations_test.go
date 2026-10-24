package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

// Reduced fixtures preserve the observed active-list and vacancy contracts,
// including the misleading alreadyApplied=false flag on a real application.
func existingTopic(id, resume string, letter bool) hh.Object {
	resources := []any{hh.Object{"type": "VACANCY", "id": "135582813"}, hh.Object{"type": "RESUME", "id": resume}}
	if letter {
		resources = append(resources, hh.Object{"type": "RESPONSE_LETTER", "id": 0})
	}
	return hh.Object{"id": id, "vacancyId": "135582813", "employerId": "125", "resumeId": resume,
		"applicantUserId":  "777",
		"initialTopicType": "RESPONSE_BY_APPLICANT",
		"chatId":           "888", "creationTime": "2026-10-17T22:00:43.726+03:00", "lastState": "RESPONSE",
		"archived": false, "applicantSubState": "SHOW", "viewedByOpponent": false,
		"hasResponseLetter": letter, "resources": resources}
}

func existingVacancy(topics []any) hh.Object {
	return hh.Object{"vacancyView": hh.Object{"vacancyFull": hh.Object{"vacancy": hh.Object{
		"vacancyId": 135582813, "name": "Android role", "company": hh.Object{"id": 125, "name": "Employer"},
		"description": "<p>Android work</p>", "status": hh.Object{"archived": false}}}},
		"applicantVacancyResponseStatuses": hh.Object{"135582813": hh.Object{
			"alreadyApplied": false, "negotiations": hh.Object{"topicList": topics},
			"resumes": hh.Object{"102": hh.Object{"hash": hashes[1]}, "103": hh.Object{"hash": hashes[2]}}}}}
}

func TestActiveApplicationListCompletenessAndIndependentIdentity(t *testing.T) {
	for _, scenario := range []string{"paginated", "empty", "duplicate-page", "wrong-filter", "truncated", "wrong-readback", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			first, second := existingTopic("501", "102", true), existingTopic("502", "103", false)
			listRequests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("read command attempted a mutation: %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/applicant/negotiations":
					listRequests++
					page := r.URL.Query().Get("page")
					if page != "" && page != "1" {
						t.Errorf("unexpected zero-based page: %q", page)
					}
					if scenario == "rate-limit" && page == "1" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					items := []any{first}
					if page == "1" && scenario != "duplicate-page" {
						items = []any{second}
					}
					filter, pages, total := "active", "2", 2
					if scenario == "empty" {
						items, pages, total = []any{}, "0", 0
					}
					if scenario == "wrong-filter" {
						filter = "deleted"
					}
					if scenario == "truncated" {
						pages = "1"
					}
					_ = json.NewEncoder(w).Encode(hh.Object{"applicantNegotiations": hh.Object{
						"filterInUse": filter, "pageCount": pages, "total": total, "topicList": items}})
				case "/vacancy/135582813":
					topics := []any{first, second}
					if scenario == "wrong-readback" {
						topics = []any{existingTopic("501", "103", true), second}
					}
					_ = json.NewEncoder(w).Encode(existingVacancy(topics))
				default:
					t.Errorf("unexpected network request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			result, err := hh.NewClient(server.Client(), base).Applications(context.Background())
			if scenario != "paginated" && scenario != "empty" {
				if err == nil || result != nil {
					t.Fatalf("misleading/partial list accepted: %v %v", result, err)
				}
				if scenario == "rate-limit" && (listRequests != 2 || !strings.Contains(err.Error(), "rate_limited")) {
					t.Fatalf("429 retried or hidden: reads=%d err=%v", listRequests, err)
				}
				return
			}
			if err != nil || result["complete"] != true {
				t.Fatalf("complete active list failed: %v %v", result, err)
			}
			rows := result["applications"].([]*hh.ApplicationStatus)
			if scenario == "empty" {
				if len(rows) != 0 || result["total"] != 0 {
					t.Fatalf("empty account invented applications: %v", result)
				}
			} else if len(rows) != 2 || rows[0].ResumeID != hashes[1] || rows[1].ResumeID != hashes[2] || !rows[0].Verified || rows[0].CoverLetter["present"] != true || rows[1].CoverLetter["present"] != false {
				t.Fatalf("wrong cross-resume identity or letter status: %+v", rows)
			}
		})
	}
}

func TestExistingApplicationAndRecordedLetterEvidence(t *testing.T) {
	const text = "Привет!\r\n\r\nAndroid work: https://example.com/app?a=1&b=2"
	for _, scenario := range []string{"confirmed", "current-text-match", "current-text-mismatch", "other-resume", "wrong-topic", "wrong-origin", "error-receipt", "missing-letter", "duplicate-receipt", "conflicting-flags"} {
		t.Run(scenario, func(t *testing.T) {
			topic := existingTopic("501", "102", scenario != "missing-letter")
			if scenario == "conflicting-flags" {
				topic["hasResponseLetter"] = false
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/vacancy/135582813" {
					t.Errorf("read/verification attempted another request: %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(existingVacancy([]any{topic}))
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			vacancy, _ := hh.NewVacancyIDFromValue("135582813")
			hash := hashes[1]
			if scenario == "other-resume" {
				hash = hashes[2]
			}
			resume, _ := hh.NewResumeIDFromValue(hash)
			status, err := hh.NewClient(server.Client(), base).Application(context.Background(), vacancy, resume)
			if scenario == "other-resume" || scenario == "conflicting-flags" {
				if err == nil {
					t.Fatal("unverified identity/flags accepted")
				}
				return
			}
			if err != nil || !status.Verified || !status.Submitted || status.TopicID != "501" || status.ResumeID != hashes[1] || status.CoverLetter["text_available"] != false {
				t.Fatalf("existing topic not independently verified: %+v %v", status, err)
			}
			target, recordedTopic, receipt := server.URL, "501", `{"success":"true"}`
			if scenario == "wrong-topic" {
				recordedTopic = "502"
			}
			if scenario == "wrong-origin" {
				target = "https://other.example"
			}
			if scenario == "error-receipt" {
				receipt = `{"error":"notEditable"}`
			}
			entry := hh.Object{"startedDateTime": "2026-10-17T19:06:07.421Z", "request": hh.Object{
				"method": "POST", "url": target + "/applicant/vacancy_response/edit_ajax", "postData": hh.Object{
					"params": []any{hh.Object{"name": "_xsrf", "value": "must-not-appear"}, hh.Object{"name": "text", "value": text}, hh.Object{"name": "topicId", "value": recordedTopic}}}},
				"response": hh.Object{"status": 200, "content": hh.Object{"text": receipt}}}
			entries := []any{entry}
			if scenario == "duplicate-receipt" {
				entries = append(entries, entry)
			}
			data, _ := json.Marshal(hh.Object{"log": hh.Object{"entries": entries}})
			path := filepath.Join(t.TempDir(), "letter.har")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(scenario, "current-text-") {
				status.CoverLetter["current_text_readback_verified"] = true
				status.CoverLetter["text"] = strings.ReplaceAll(text, "\r\n", "\n")
				if scenario == "current-text-mismatch" {
					status.CoverLetter["text"] = "A different current letter"
				}
			}
			err = hh.VerifyRecordedLetter(status, path)
			if requests != 1 {
				t.Fatalf("HAR was replayed or verification sent a request: %d", requests)
			}
			if scenario != "confirmed" && scenario != "current-text-match" {
				if err == nil || status.RecordedLetter != nil {
					t.Fatalf("unbound receipt accepted: %+v %v", status.RecordedLetter, err)
				}
				return
			}
			if err != nil || status.RecordedLetter.Text != text || !status.RecordedLetter.Accepted || !status.RecordedLetter.LivePresence || status.RecordedLetter.TextReadback != (scenario == "current-text-match") || status.CoverLetter["ui_add_available"] != false {
				t.Fatalf("letter receipt/presence was lost or overstated: %+v %v", status, err)
			}
			encoded, _ := json.Marshal(status)
			if strings.Contains(string(encoded), "must-not-appear") {
				t.Fatal("HAR verification exposed credentials")
			}
		})
	}
}
