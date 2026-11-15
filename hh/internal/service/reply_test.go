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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestApplicationReplyApprovalAndReadback(t *testing.T) {
	const answer = "Здравствуйте!\nПроверял права доступа и время выполнения."
	for _, scenario := range []string{"preview", "send", "unapproved", "changed-text", "new-message", "write-disabled", "ignored-write", "wrong-author", "wrong-text", "rate-limit"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			posts := 0
			allowed := true
			messages := []any{hh.Object{"id": "1000", "chatId": "888", "participantId": "778", "hidden": false, "text": "Как проверяли результат?", "type": "SIMPLE"}}
			backupDir := t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/vacancy/135582813":
					_ = json.NewEncoder(w).Encode(existingVacancy([]any{existingTopic("501", "102", true)}))
				case "/chat/888":
					if r.Method != http.MethodGet {
						t.Errorf("chat read mutated state: %s", r.Method)
					}
					state := hh.Object{"chatData": hh.Object{
						"chat": hh.Object{"id": "888", "type": "NEGOTIATION", "currentParticipantId": "777",
							"resources": hh.Object{"NEGOTIATION_TOPIC": []any{"501"}, "VACANCY": []any{"135582813"}, "RESUME": []any{"102"}},
							"messages":  hh.Object{"hasMore": false, "items": messages}},
						"chatStates": hh.Object{"writeMessageState": hh.Object{"allowed": allowed}},
						"resources":  hh.Object{"negotiation_topics": hh.Object{"501": hh.Object{"topicId": "501", "vacancyId": "135582813", "resumeId": "102"}}, "resumeHashById": hh.Object{"102": hashes[1]}},
					}}
					data, _ := json.Marshal(state)
					fmt.Fprintf(w, `<template class="Chatik-InitialState">%s</template>`, html.EscapeString(string(data)))
				case "/chatik/api/send":
					posts++
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Xsrftoken") != "fixture-token" || r.URL.Query().Get("hhtmSourceLabel") != "chat" {
						t.Errorf("native reply transport differs: %s %s %v", r.Method, r.URL, r.Header)
					}
					var request struct {
						ChatID         int64  `json:"chatId"`
						IdempotencyKey string `json:"idempotencyKey"`
						Text           string `json:"text"`
					}
					decoder := json.NewDecoder(r.Body)
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&request); err != nil || request.ChatID != 888 || request.Text != answer || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(request.IdempotencyKey) {
						t.Errorf("invalid native reply body: %+v %v", request, err)
						http.Error(w, "invalid reply", 400)
						return
					}
					files, _ := filepath.Glob(filepath.Join(backupDir, "*.json"))
					if len(files) != 1 {
						t.Error("reply sent without a saved pre-send backup")
					} else {
						data, _ := os.ReadFile(files[0])
						info, _ := os.Stat(files[0])
						var snapshot struct {
							Request struct {
								Text           string `json:"text"`
								IdempotencyKey string `json:"idempotencyKey"`
							} `json:"request"`
						}
						if json.Unmarshal(data, &snapshot) != nil || snapshot.Request.Text != answer || snapshot.Request.IdempotencyKey != request.IdempotencyKey || info == nil || info.Mode().Perm() != 0600 {
							t.Error("backup lost exact request data or private permissions")
						}
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(http.StatusTooManyRequests)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					if scenario != "ignored-write" {
						message := hh.Object{"id": "1002", "chatId": "888", "participantId": "777", "hidden": false, "text": request.Text, "type": "SIMPLE"}
						if scenario == "wrong-author" {
							message["participantId"] = "778"
						}
						if scenario == "wrong-text" {
							message["text"] = "Другой текст"
						}
						messages = append(messages, message)
					}
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
			vacancy, _ := hh.NewVacancyIDFromValue("135582813")
			resume, _ := hh.NewResumeIDFromValue(hashes[1])
			status, err := client.Application(context.Background(), vacancy, resume)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := client.ReplyApplicationMessage(context.Background(), status, answer, false, "", backupDir)
			if err != nil {
				t.Fatal(err)
			}
			if posts != 0 || preview["text"] != answer || preview["chat_id"] != "888" || preview["resume_id"] != hashes[1] || preview["after_message_id"] != "1000" || preview["sent"] != false {
				t.Fatalf("preview mutated or lost the reply package: %v, posts=%d", preview, posts)
			}
			if scenario == "preview" {
				return
			}
			approval := preview["review_sha256"].(string)
			text := " \r\n" + strings.ReplaceAll(answer, "\n", "\r\n") + "\r\n"
			mu.Lock()
			if scenario == "new-message" {
				messages = append(messages, hh.Object{"id": "1001", "chatId": "888", "participantId": "778", "hidden": false, "text": "Уточнение к вопросу", "type": "SIMPLE"})
			} else if scenario == "write-disabled" {
				allowed = false
			}
			mu.Unlock()
			if scenario == "unapproved" {
				approval = ""
			} else if scenario == "changed-text" {
				text = answer + " Изменено без согласования."
			}
			result, err := client.ReplyApplicationMessage(context.Background(), status, text, true, approval, backupDir)
			mu.Lock()
			defer mu.Unlock()
			wantPosts := 1
			if scenario == "unapproved" || scenario == "changed-text" || scenario == "new-message" || scenario == "write-disabled" {
				wantPosts = 0
			}
			if posts != wantPosts {
				t.Fatalf("unapproved send or retry: posts=%d want=%d", posts, wantPosts)
			}
			if scenario == "send" {
				if err != nil || result["sent"] != true || result["verified"] != true || result["message_id"] != "1002" || result["text"] != answer {
					t.Fatalf("reply did not verify exact delivery: %v %v", result, err)
				}
			} else if err == nil || result != nil {
				t.Fatalf("refused or unverified reply accepted: %v %v", result, err)
			}
		})
	}
}
