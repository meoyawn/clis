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
	"sync"
	"testing"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

// A direct recruiter chat has no application; the reply is bound to the chat,
// its last visible message and the exact text, and verified as the applicant's.
func TestDirectChatReplyApprovalAndReadback(t *testing.T) {
	const answer = "Добрый день!\nВакансия ещё открыта?"
	for _, scenario := range []string{"preview", "send", "new-message", "write-disabled", "wrong-author"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			posts := 0
			allowed := true
			message := func(id, author, text string) hh.Object {
				return hh.Object{"id": id, "chatId": "555", "participantId": author, "hidden": false, "text": text, "type": "SIMPLE",
					"creationTime": "2026-11-09T10:11:34+03:00", "participantDisplay": hh.Object{"name": map[string]string{"777": "Fixture Applicant", "778": "Fixture Recruiter"}[author]}}
			}
			messages := []any{message("1000", "778", "Посмотрите вакансию")}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/chat/555":
					state := hh.Object{"chatData": hh.Object{
						"chat":       hh.Object{"id": "555", "type": "SIMPLE", "currentParticipantId": "777", "messages": hh.Object{"hasMore": false, "items": messages}},
						"chatStates": hh.Object{"writeMessageState": hh.Object{"allowed": allowed}},
					}}
					data, _ := json.Marshal(state)
					fmt.Fprintf(w, `<template class="Chatik-InitialState">%s</template>`, html.EscapeString(string(data)))
				case "/chatik/api/send":
					posts++
					var request struct {
						ChatID int64  `json:"chatId"`
						Text   string `json:"text"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.ChatID != 555 || request.Text != answer {
						t.Errorf("reply posted to the wrong chat or with changed text: %+v %v", request, err)
					}
					author := "777"
					if scenario == "wrong-author" {
						author = "778"
					}
					messages = append(messages, message("1002", author, request.Text))
					fmt.Fprint(w, `{"id":1002}`)
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
			id, _ := domain.NewChatIDFromValue("555")
			backupDir := t.TempDir()
			preview, err := client.ReplyChat(context.Background(), id, answer, false, "", backupDir)
			if err != nil {
				t.Fatal(err)
			}
			if posts != 0 || preview["text"] != answer || preview["after_message_id"] != "1000" || preview["sent"] != false || fmt.Sprint(preview["recipients"]) != "[Fixture Recruiter]" {
				t.Fatalf("preview sent or lost the destination: %v posts=%d", preview, posts)
			}
			if scenario == "preview" {
				return
			}
			mu.Lock()
			if scenario == "new-message" {
				messages = append(messages, message("1001", "778", "Уточнение"))
			} else if scenario == "write-disabled" {
				allowed = false
			}
			mu.Unlock()
			result, err := client.ReplyChat(context.Background(), id, "\r\n"+answer+"\r\n", true, preview["review_sha256"].(string), backupDir)
			wantPosts := map[string]int{"send": 1, "wrong-author": 1}[scenario]
			if posts != wantPosts {
				t.Fatalf("posts=%d want=%d", posts, wantPosts)
			}
			if scenario == "send" {
				if err != nil || result["verified"] != true || result["message_id"] != "1002" {
					t.Fatalf("delivery not verified: %v %v", result, err)
				}
			} else if err == nil || result != nil {
				t.Fatalf("refused or unverified reply accepted: %v", result)
			}
		})
	}
}
