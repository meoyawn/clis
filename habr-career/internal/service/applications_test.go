package service_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/service"
)

func TestApplicationListPaginatesBindsAccountAndRejectsPartialResults(t *testing.T) {
	for _, scenario := range []string{"empty", "pages", "wrong-account", "invalid-role", "duplicate", "foreign-next", "missing-row", "unknown-empty-state"} {
		t.Run(scenario, func(t *testing.T) {
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("list attempted a write: %s %s", r.Method, r.URL)
				}
				if r.URL.Path == "/api/frontend_v1/users/me" {
					fmt.Fprint(w, `{"user":{"alias":"synthetic-user","email":"synthetic@example.test"}}`)
					return
				}
				if r.URL.Path != "/responses" {
					t.Errorf("unexpected list request %s", r.URL)
					http.NotFound(w, r)
					return
				}
				pages++
				alias, total := "synthetic-user", 3
				if scenario == "wrong-account" {
					alias = "another-user"
				}
				if scenario == "empty" || scenario == "unknown-empty-state" {
					total = 0
				}
				fmt.Fprintf(w, `<body class="responses_index_page"><a id="top_avatar" href="/%s">Profile</a><a class="tab-item tab-item--active" href="/responses">Основные (%d)</a>`, alias, total)
				if total == 0 {
					title := "Нет откликов"
					if scenario == "unknown-empty-state" {
						title = "Сервис недоступен"
					}
					fmt.Fprintf(w, `<div class="no-content__title">%s</div></body>`, title)
					return
				}
				fmt.Fprint(w, `<table class="my_responses"><tr><th>Позиция</th><th>Отклик</th><th>Статус</th></tr>`)
				ids := []string{"1000000001", "1000000002"}
				status := "Не прочитано"
				if r.URL.Query().Get("page") == "2" {
					ids, status = []string{"1000000003"}, "Просмотрено"
					if scenario == "duplicate" {
						ids = []string{"1000000001"}
					}
					if scenario == "missing-row" {
						ids = nil
					}
				}
				for _, id := range ids {
					if scenario == "invalid-role" {
						id = "not-an-id"
					}
					fmt.Fprintf(w, `<tr><td><div class="title"><a href="/vacancies/%s">Synthetic role %s</a></div><div class="meta"><a href="/companies/synthetic-employer">Synthetic Employer</a></div></td><td class="created_at">04.11.2026</td><td><div class="status">%s</div></td></tr>`, id, id, status)
				}
				fmt.Fprint(w, `</table><div id="paginator">`)
				if r.URL.Query().Get("page") != "2" {
					href := "/responses?page=2"
					if scenario == "foreign-next" {
						href = "https://foreign.test/responses?page=2"
					}
					fmt.Fprintf(w, `<a href="%s" rel="next">Следующая</a>`, href)
				} else {
					fmt.Fprint(w, `<a href="/responses?page=1" rel="prev">Предыдущая</a>`)
				}
				fmt.Fprint(w, `</div></body>`)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			result, err := service.NewClient(httpClient).Applications(t.Context())
			if scenario != "empty" && scenario != "pages" {
				if err == nil || result != nil {
					t.Fatalf("unsafe/partial list reported success: %v %v", result, err)
				}
				return
			}
			if err != nil || result["account_alias"] != "synthetic-user" {
				t.Fatalf("list = %v %v", result, err)
			}
			rows := result["applications"].([]any)
			if scenario == "empty" {
				if len(rows) != 0 || pages != 1 {
					t.Fatalf("empty list = %v pages=%d", result, pages)
				}
				return
			}
			if len(rows) != 3 || pages != 2 {
				t.Fatalf("pagination incomplete: %v pages=%d", result, pages)
			}
			row := rows[2].(service.Object)
			if row["vacancy_id"] != "1000000003" || row["applied_on"] != "2026-11-04" || row["status"] != "Просмотрено" || !strings.HasSuffix(row["company_url"].(string), "/companies/synthetic-employer") {
				t.Fatalf("row identity/date/status lost: %v", row)
			}
		})
	}
}
