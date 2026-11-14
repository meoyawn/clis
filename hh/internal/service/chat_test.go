package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestLiveCoverLetterReadbackBindsChatMessageAndApplication(t *testing.T) {
	const letter = "Привет!\n\nOffline-first: https://example.com/app?a=1&b=2\n\nAndroid work."
	for _, scenario := range []string{"live", "wrong-chat", "wrong-topic", "wrong-resume", "wrong-author", "ordinary-message", "message-outside-page", "multiple-letters", "rate-limit", "wrong-template", "messages", "messages-without-letter", "messages-older-history", "messages-foreign", "messages-hidden", "deleted-chat", "deleted-wrong-resume", "deleted-wrong-employer", "deleted-wrong-outcome", "deleted-no-public-hash"} {
		t.Run(scenario, func(t *testing.T) {
			deletedMode := strings.HasPrefix(scenario, "deleted-")
			messageMode := strings.HasPrefix(scenario, "messages") || deletedMode
			linked := func() hh.Object {
				return hh.Object{"NEGOTIATION_TOPIC": []any{"501"}, "VACANCY": []any{"135582813"}, "RESUME": []any{"102"}, "RESPONSE_LETTER": []any{"0"}}
			}
			message := hh.Object{"id": "999", "chatId": "888", "participantId": "777", "hidden": false, "hasContent": true,
				"type": "SIMPLE", "canEdit": true, "text": letter, "resources": linked(), "workflowTransition": hh.Object{"topicId": "501"}}
			items := []any{message}
			if messageMode {
				delete(message["resources"].(map[string]any), "RESPONSE_LETTER")
				items = append(items, hh.Object{"id": "1000", "chatId": "888", "participantId": "778", "hidden": false,
					"text": "Нужен опыт в предметной области.", "type": "SIMPLE", "creationTime": "2026-11-14T15:00:00Z",
					"workflowTransition": hh.Object{"topicId": "501", "applicantState": "DISCARD"}})
			}
			if scenario == "messages-foreign" {
				message["chatId"] = "889"
			}
			if scenario == "messages-hidden" {
				message["hidden"] = true
			}
			if scenario == "wrong-author" {
				message["participantId"] = "778"
			}
			if scenario == "ordinary-message" {
				delete(message["resources"].(map[string]any), "RESPONSE_LETTER")
			}
			if scenario == "message-outside-page" {
				items = []any{}
			}
			if scenario == "multiple-letters" {
				items = append(items, message)
			}
			chat := hh.Object{"id": "888", "type": "NEGOTIATION", "currentParticipantId": "777", "resources": linked(),
				"messages": hh.Object{"items": items, "hasMore": scenario == "message-outside-page" || scenario == "messages-older-history"}}
			if scenario == "wrong-chat" {
				chat["id"] = "889"
			}
			topic := hh.Object{"topicId": "501", "vacancyId": "135582813", "resumeId": "102", "currentApplicantState": "DISCARD"}
			chat["ownerEmployerInfo"] = hh.Object{"id": "125"}
			if scenario == "deleted-wrong-resume" {
				topic["resumeId"] = "103"
				chat["resources"].(map[string]any)["RESUME"] = []any{"102", "103"}
			}
			if scenario == "deleted-wrong-employer" {
				chat["ownerEmployerInfo"] = hh.Object{"id": "126"}
			}
			if scenario == "deleted-wrong-outcome" {
				topic["currentApplicantState"] = "RESPONSE"
			}
			if scenario == "wrong-topic" {
				topic["topicId"] = "502"
			}
			hash := hashes[1]
			if scenario == "wrong-resume" {
				hash = hashes[2]
			}
			chatState := hh.Object{"chatData": hh.Object{"chat": chat, "resources": hh.Object{
				"negotiation_topics": hh.Object{"501": topic}, "resumeHashById": hh.Object{"102": hash}}}}
			if scenario == "deleted-no-public-hash" {
				chatState["chatData"].(map[string]any)["resources"].(map[string]any)["resumeHashById"] = hh.Object{}
			}
			chatReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.ContentLength > 0 {
					t.Errorf("letter read attempted to mutate state: %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/applicant/negotiations":
					row := existingTopic("501", "102", false)
					row["archived"], row["applicantSubState"], row["lastState"] = true, "HIDE", "DISCARD"
					_ = json.NewEncoder(w).Encode(hh.Object{"applicantNegotiations": hh.Object{"filterInUse": "deleted", "pageCount": "1", "total": 1, "topicList": []any{row}}})
				case "/vacancy/135582813":
					if deletedMode {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(existingVacancy([]any{existingTopic("501", "102", scenario != "messages-without-letter")}))
				case "/chat/888":
					chatReads++
					if r.URL.Query().Get("without_list") != "1" || r.URL.Query().Get("platform") != "xhh" {
						t.Errorf("changed recorded chat read contract: %s", r.URL)
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					class := "Chatik-InitialState"
					if scenario == "wrong-template" {
						class = "Other-InitialState"
					}
					data, _ := json.Marshal(chatState)
					fmt.Fprintf(w, `<template style="display:none" class="%s">%s</template>`, class, html.EscapeString(string(data)))
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := hh.NewClient(server.Client(), base)
			vacancy, _ := hh.NewVacancyIDFromValue("135582813")
			resume, _ := hh.NewResumeIDFromValue(hashes[1])
			var status *hh.ApplicationStatus
			var err error
			if deletedMode {
				var result hh.Object
				result, err = client.ApplicationsFiltered(context.Background(), domain.DeletedNegotiations(), false)
				if err == nil {
					status = result["applications"].([]*hh.ApplicationStatus)[0]
					if status.Verified || status.VacancyHTTP != 404 {
						t.Fatal("missing vacancy was marked verified before retained-chat readback")
					}
				}
			} else {
				status, err = client.Application(context.Background(), vacancy, resume)
			}
			if err != nil {
				t.Fatal(err)
			}
			if messageMode {
				err = client.ReadApplicationMessages(context.Background(), status)
			} else {
				err = client.ReadApplicationLetter(context.Background(), status)
			}
			if chatReads != 1 {
				t.Fatalf("chat read repeated or missing: %d", chatReads)
			}
			if messageMode {
				if scenario == "messages-foreign" || strings.HasPrefix(scenario, "deleted-wrong-") {
					if err == nil || status.Messages != nil {
						t.Fatalf("foreign chat message accepted: %v %v", status.Messages, err)
					}
					if deletedMode && !strings.Contains(err.Error(), "deleted topic") {
						t.Fatalf("deleted-topic binding failed for an unrelated reason: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				visible := status.Messages["items"].([]hh.Object)
				wantCount := 2
				if scenario == "messages-hidden" {
					wantCount = 1
				}
				if len(visible) != wantCount || visible[len(visible)-1]["text"] != "Нужен опыт в предметной области." || visible[len(visible)-1]["from_applicant"] != false || status.Messages["has_more"] != (scenario == "messages-older-history") {
					t.Fatalf("feedback, attribution, hidden-message filtering or completeness lost: %v", status.Messages)
				}
				if deletedMode && (!status.Verified || status.NativeResumeID != "102" || (status.ResumeID == "") != (scenario == "deleted-no-public-hash")) {
					t.Fatalf("retained-chat verification or unknown hash lost: %+v", status)
				}
				return
			}
			if scenario != "live" {
				if err == nil || status.CoverLetter["text_available"] != false || status.CoverLetter["text"] != nil {
					t.Fatalf("wrong or unverified message exposed as cover letter: %v %v", status.CoverLetter, err)
				}
				return
			}
			if err != nil || status.CoverLetter["text"] != letter || status.CoverLetter["current_text_readback_verified"] != true || status.CoverLetter["can_edit_reported_by_chat"] != true || status.CoverLetter["message_id"] != "999" {
				t.Fatalf("live text/identity/edit metadata lost: %v %v", status.CoverLetter, err)
			}
		})
	}
}
