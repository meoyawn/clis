package service_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"github.com/meoyawn/clis/habr-career/internal/service"
)

func TestCompanyVacanciesBindsEmployerAndRejectsPartialLists(t *testing.T) {
	for _, scenario := range []string{"cards", "empty", "paginated", "foreign-card", "wrong-alias", "duplicate", "no-state"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/companies/synthetic/vacancies" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				alias := "synthetic"
				if scenario == "wrong-alias" {
					alias = "another"
				}
				if scenario != "no-state" {
					fmt.Fprintf(w, `<script type="application/json" data-ssr-state="true">{"companyAlias":%q,"companyId":1000000009,"isCompanyVacanciesPage":true}</script>`, alias)
				}
				ids := []string{"1000000001", "1000000002"}
				if scenario == "empty" {
					ids = nil
					fmt.Fprint(w, `<div class="no-content__title">Нет вакансий</div>`)
				}
				if scenario == "duplicate" {
					ids = []string{"1000000001", "1000000001"}
				}
				for i, id := range ids {
					employer := "/companies/synthetic"
					if scenario == "foreign-card" && i == 1 {
						employer = "/companies/foreign"
					}
					fmt.Fprintf(w, `<div class="vacancy-card"><time class="basic-date" datetime="2026-11-16T16:18:30+03:00">16 ноября</time><div class="vacancy-card__company"><a href="%s">Synthetic</a></div><div class="vacancy-card__title"><a class="vacancy-card__title-link" href="/vacancies/%s">Role %s</a></div><div class="vacancy-card__meta"><div class="chip-with-icon__text">Можно удалённо</div></div><div class="vacancy-card__skills"><a class="basic-chip"><div class="basic-chip__text">Go</div></a></div></div>`, employer, id, id)
				}
				fmt.Fprint(w, `<div id="paginator">`)
				if scenario == "paginated" {
					fmt.Fprint(w, `<a href="/companies/synthetic/vacancies?page=2">2</a>`)
				}
				fmt.Fprint(w, `</div>`)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			httpClient := server.Client()
			httpClient.Transport = routeTransport{target: target, transport: httpClient.Transport}
			company, err := domain.NewCompanyIDFromValue("https://career.habr.com/companies/synthetic/vacancies")
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.NewClient(httpClient).CompanyVacancies(t.Context(), company)
			if scenario != "cards" && scenario != "empty" {
				if err == nil || result != nil {
					t.Fatalf("unsafe/partial list reported success: %v %v", result, err)
				}
				return
			}
			if err != nil || result["company_id"] != "1000000009" {
				t.Fatalf("list = %v %v", result, err)
			}
			rows := result["vacancies"].([]any)
			if scenario == "empty" {
				if len(rows) != 0 {
					t.Fatalf("empty list = %v", rows)
				}
				return
			}
			row := rows[1].(service.Object)
			if len(rows) != 2 || row["id"] != "1000000002" || row["url"] != "https://career.habr.com/vacancies/1000000002" || row["published_at"] != "2026-11-16T16:18:30+03:00" || fmt.Sprint(row["skills"]) != "[Go]" || fmt.Sprint(row["meta"]) != "[Можно удалённо]" {
				t.Fatalf("card fields lost: %v", rows)
			}
		})
	}
}
