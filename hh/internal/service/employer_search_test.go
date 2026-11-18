package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

func TestSearchExactEmployerAndCurrentProfessionalCategory(t *testing.T) {
	for _, scenario := range []string{"employer", "category", "wrong-employer-criteria", "wrong-result-employer", "wrong-role-criteria", "missing-category", "duplicate-role"} {
		t.Run(scenario, func(t *testing.T) {
			options := domain.VacancySearchOptions{Employers: []string{"201"}, PerPage: 100}
			if scenario != "employer" {
				options.ProfessionalArea = "11"
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				want := url.Values{"text": {""}, "page": {"0"}, "items_on_page": {"100"}, "employer_id": {"201"}}
				if requests == 2 {
					want["professional_role"] = []string{"96", "160"}
				}
				if r.Method != http.MethodGet || r.URL.Path != "/search/vacancy" || !reflect.DeepEqual(r.URL.Query(), want) {
					t.Errorf("unexpected native employer/category request: %s %s", r.Method, r.URL)
				}
				row := searchRow(101, "Example developer")
				state := searchPage("", []any{row}, 100)
				list := state["vacancySearchResult"].(hh.Object)
				list["totalResults"], list["paging"] = 1, nil
				criteria := list["criteria"].(hh.Object)
				criteria["work_format"], criteria["employer_id"] = []any{}, []any{201}
				roles := []any{hh.Object{"id": "96", "text": "Developer"}, hh.Object{"id": "160", "text": "DevOps"}}
				if scenario == "duplicate-role" {
					roles[1] = roles[0]
				}
				state["professionalRoleTree"] = hh.Object{"items": []any{hh.Object{"id": "category-11", "text": "Information technology", "items": roles}}}
				if scenario == "missing-category" {
					delete(state, "professionalRoleTree")
				}
				if requests == 2 {
					criteria["professional_role"] = []any{96, 160}
					switch scenario {
					case "wrong-employer-criteria":
						criteria["employer_id"] = []any{202}
					case "wrong-result-employer":
						row["company"] = hh.Object{"id": 202, "name": "Wrong employer"}
					case "wrong-role-criteria":
						criteria["professional_role"] = []any{96}
					}
				}
				_ = json.NewEncoder(w).Encode(state)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			result, err := hh.NewClient(server.Client(), base).SearchVacancies(context.Background(), options)
			wantRequests := 2
			if scenario == "employer" || scenario == "missing-category" || scenario == "duplicate-role" {
				wantRequests = 1
			}
			if requests != wantRequests {
				t.Fatalf("unexpected reads after success/failure: got %d, want %d", requests, wantRequests)
			}
			if scenario != "employer" && scenario != "category" {
				if err == nil || result != nil {
					t.Fatalf("unverified employer/category results accepted: %+v, %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Vacancies) != 1 || result.Vacancies[0].Employer.ID != "201" || result.TotalCandidates != 1 || result.NextPage != nil {
				t.Fatalf("incomplete employer result: %+v", result)
			}
			if scenario == "category" && (result.ProfessionalArea == nil || result.ProfessionalArea.ID != "11" || !reflect.DeepEqual(result.ProfessionalArea.RoleIDs, []string{"96", "160"})) {
				t.Fatalf("lost current category membership: %+v", result.ProfessionalArea)
			}
		})
	}
}
