package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestLiveCoverLetterReadbackBindsChatMessageAndApplication(t *testing.T) {
	const letter = "Привет!\n\nOffline-first: https://example.com/app?a=1&b=2\n\nAndroid work."
	for _, scenario := range []string{"live", "wrong-chat", "wrong-topic", "wrong-resume", "wrong-author", "ordinary-message", "message-outside-page", "multiple-letters", "rate-limit", "wrong-template"} {
		t.Run(scenario, func(t *testing.T) {
			linked := func() hh.Object {
				return hh.Object{"NEGOTIATION_TOPIC": []any{"501"}, "VACANCY": []any{"135582813"}, "RESUME": []any{"102"}, "RESPONSE_LETTER": []any{"0"}}
			}
			message := hh.Object{"id": "999", "chatId": "888", "participantId": "777", "hidden": false, "hasContent": true,
				"type": "SIMPLE", "canEdit": true, "text": letter, "resources": linked(), "workflowTransition": hh.Object{"topicId": "501"}}
			items := []any{message}
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
				"messages": hh.Object{"items": items, "hasMore": scenario == "message-outside-page"}}
			if scenario == "wrong-chat" {
				chat["id"] = "889"
			}
			topic := hh.Object{"topicId": "501", "vacancyId": "135582813", "resumeId": "102"}
			if scenario == "wrong-topic" {
				topic["topicId"] = "502"
			}
			hash := hashes[1]
			if scenario == "wrong-resume" {
				hash = hashes[2]
			}
			chatState := hh.Object{"chatData": hh.Object{"chat": chat, "resources": hh.Object{
				"negotiation_topics": hh.Object{"501": topic}, "resumeHashById": hh.Object{"102": hash}}}}
			chatReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.ContentLength > 0 {
					t.Errorf("letter read attempted to mutate state: %s %s", r.Method, r.URL)
					w.WriteHeader(500)
					return
				}
				switch r.URL.Path {
				case "/vacancy/135582813":
					_ = json.NewEncoder(w).Encode(existingVacancy([]any{existingTopic("501", "102", true)}))
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
			status, err := client.Application(context.Background(), vacancy, resume)
			if err != nil {
				t.Fatal(err)
			}
			err = client.ReadApplicationLetter(context.Background(), status)
			if chatReads != 1 {
				t.Fatalf("chat read repeated or missing: %d", chatReads)
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
