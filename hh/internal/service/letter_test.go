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
	"strings"
	"sync"
	"testing"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestUpdateApplicationLetter(t *testing.T) {
	const before = "Предыдущее сопроводительное."
	const replacement = "7 лет занимаюсь собственными продуктами.\n\nПромпт:\nРазработай функцию."
	for _, scenario := range []string{"preview", "apply", "unchanged", "not-editable", "ignored-write", "rate-limit", "empty"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			current, posts := before, 0
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
					linked := hh.Object{"NEGOTIATION_TOPIC": []any{"501"}, "VACANCY": []any{"135582813"}, "RESUME": []any{"102"}, "RESPONSE_LETTER": []any{"0"}}
					state := hh.Object{"chatData": hh.Object{
						"chat": hh.Object{"id": "888", "type": "NEGOTIATION", "currentParticipantId": "777", "resources": linked,
							"messages": hh.Object{"hasMore": false, "items": []any{hh.Object{
								"id": "999", "chatId": "888", "participantId": "777", "hidden": false, "hasContent": true,
								"text": current, "canEdit": scenario != "not-editable", "resources": linked, "workflowTransition": hh.Object{"topicId": "501"},
							}}}},
						"resources": hh.Object{"negotiation_topics": hh.Object{"501": hh.Object{"topicId": "501", "vacancyId": "135582813", "resumeId": "102"}}, "resumeHashById": hh.Object{"102": hashes[1]}},
					}}
					data, _ := json.Marshal(state)
					fmt.Fprintf(w, `<template class="Chatik-InitialState">%s</template>`, html.EscapeString(string(data)))
				case "/chatik/api/save":
					posts++
					if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Xsrftoken") != "fixture-token" {
						t.Errorf("native edit transport or CSRF contract differs: %s %v", r.Method, r.Header)
					}
					var request struct {
						Text      string `json:"text"`
						MessageID int64  `json:"messageId"`
					}
					decoder := json.NewDecoder(r.Body)
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&request); err != nil || request.MessageID != 999 || request.Text != replacement {
						t.Errorf("wrong message or text sent: %+v %v", request, err)
						http.Error(w, "invalid edit", 400)
						return
					}
					files, err := filepath.Glob(filepath.Join(backupDir, "*.json"))
					if err != nil || len(files) != 1 {
						t.Errorf("previous letter was not backed up before sending: %v %v", files, err)
					} else {
						data, _ := os.ReadFile(files[0])
						var snapshot hh.ApplicationStatus
						info, _ := os.Stat(files[0])
						if json.Unmarshal(data, &snapshot) != nil || snapshot.CoverLetter["text"] != before || info == nil || info.Mode().Perm() != 0600 {
							t.Error("backup lost the previous text or its private permissions")
						}
					}
					if scenario == "rate-limit" {
						w.Header().Set("Retry-After", "60")
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":"rate_limited"}`)
						return
					}
					if scenario != "ignored-write" {
						current = request.Text
					}
					w.WriteHeader(204)
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
			text := strings.ReplaceAll(replacement, "\n", "\r\n") + "\r\n"
			if scenario == "unchanged" {
				text = before
			} else if scenario == "empty" {
				text = " \r\n "
			}
			result, err := client.UpdateApplicationLetter(context.Background(), status, text, scenario != "preview", backupDir)
			mu.Lock()
			defer mu.Unlock()
			wantError := map[string]string{"not-editable": "not confirmed editable", "ignored-write": "readback differs", "rate-limit": "rate_limited", "empty": "nonempty"}[scenario]
			wantPosts := 0
			if scenario == "apply" || scenario == "ignored-write" || scenario == "rate-limit" {
				wantPosts = 1
			}
			if posts != wantPosts {
				t.Fatalf("preview/refusal wrote or an edit was retried: posts=%d want=%d", posts, wantPosts)
			}
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) || result != nil || current != before {
					t.Fatalf("unverified or refused edit accepted: result=%v current=%q err=%v", result, current, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "apply" {
				if current != replacement || result["text"] != replacement || result["previous_text"] != before || result["applied"] != true || result["verified"] != true || result["message_id"] != "999" {
					t.Fatalf("edit did not preserve identity and verify the saved text: %v", result)
				}
			} else if current != before || result["applied"] != false || (scenario == "unchanged" && result["changed"] != false) {
				t.Fatalf("preview or no-op changed the saved letter: %v", result)
			}
		})
	}
}
