package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

// Synthetic public offers use the observed /api/offers wrapper. Extra private
// fields deliberately do not belong to the CLI's search output contract.
func searchOfferFixture(id int, skills any) Object {
	return Object{
		"id": id, "position": "Golang developer", "url": fmt.Sprintf("/vacancies/%d-backend", id),
		"is_active": true, "published_at": "2026-11-14T10:00:00",
		"company":        Object{"id": nil, "name": "Example employer", "url": "/companies/example"},
		"location_items": []any{Object{"label": "Россия", "format": "remote", "exclude": false}},
		"skills_objects": skills, "offer_description": "Go and Golang in prose do not establish a skill tag.",
		"salary_hidden": false, "salary_display_from": 350000, "salary_display_to": 450000,
		"salary_currency": "RUB", "salary_taxes": "gross", "salary_is_total": true,
		"salary_by_our_version": true, "salary_from": "PRIVATE_SENTINEL",
		"application": Object{"cover_letter": "PRIVATE_SENTINEL"},
	}
}

func searchPageFixture(offers []any, total, offset, limit int) Object {
	return Object{
		"meta": Object{"total": total, "offset": offset, "limit": limit}, "offers": offers,
		"filters":         Object{"sp": []any{Object{"value": "golang"}, Object{"value": "python"}}},
		"profile_filters": Object{"salary": "PRIVATE_SENTINEL", "locations": []string{"PRIVATE_SENTINEL"}},
	}
}

func TestSearchVacanciesNativeFiltersAndExactTags(t *testing.T) {
	offers := []any{
		searchOfferFixture(101, []any{Object{"name": " Go ", "slug": "go"}, Object{"name": "PostgreSQL"}}),
		searchOfferFixture(102, []any{Object{"name": "GOLANG", "slug": nil, "is_custom": true}}),
		searchOfferFixture(103, []any{Object{"name": "Rust"}}),
		searchOfferFixture(104, []any{Object{"name": "GoCD"}, Object{"name": "Django"}}),
		searchOfferFixture(105, nil),
		searchOfferFixture(106, []any{Object{"name": "Go"}}),
	}
	offers[1].(Object)["salary_hidden"] = true
	offers[5].(Object)["is_active"] = false
	offers[0].(Object)["location_items"] = []any{
		Object{"label": "Москва", "format": "hybrid", "exclude": false},
		Object{"label": "Россия", "format": "remote", "exclude": false},
		Object{"label": "Исключённая страна", "format": "remote", "exclude": true},
	}
	calls := 0
	client := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		want := url.Values{
			"p": {"1"}, "offset": {"0"}, "limit": {"6"}, "pa": {"all"}, "sa": {"350000"},
			"l": {"remote"}, "sp": {"golang", "python"}, "se": {"senior", "lead"},
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/offers" || !reflect.DeepEqual(r.URL.Query(), want) || r.Header.Get("Referer") != Origin+"/vacancies" {
			t.Fatalf("native search request changed: %s %s", r.Method, r.URL)
		}
		data, err := json.Marshal(searchPageFixture(offers, 8, 0, 6))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}, nil)
	result, err := client.SearchVacancies(context.Background(), domain.VacancySearchOptions{
		Specializations: []string{"golang", "python"}, Skills: []string{"go", " Golang "},
		Remote: true, Salary: 350000, Levels: []domain.Level{domain.Senior(), domain.Lead()}, PerPage: 6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result.TotalCandidates != 8 || result.ScannedCandidates != 6 || result.Page != 0 || result.NextPage == nil || *result.NextPage != 1 {
		t.Fatalf("local filtering changed candidate pagination or made extra requests: calls=%d result=%+v", calls, result)
	}
	if len(result.Vacancies) != 2 || result.Vacancies[0].ID != "101" || result.Vacancies[1].ID != "102" {
		t.Fatalf("prose, partial tags or inactive vacancies passed the skill filter: %+v", result.Vacancies)
	}
	first, hidden := result.Vacancies[0], result.Vacancies[1]
	if first.URL != Origin+"/vacancies/101-backend" || first.Employer.Name != "Example employer" || first.Employer.ID != nil || first.Employer.URL != Origin+"/companies/example" {
		t.Fatalf("public identity lost or invented: %+v", first)
	}
	if first.Compensation.From == nil || *first.Compensation.From != 350000 || first.Compensation.To == nil || *first.Compensation.To != 450000 || first.Compensation.Taxes == nil || *first.Compensation.Taxes != "gross" || first.Compensation.Currency == nil || *first.Compensation.Currency != "RUB" || !first.Compensation.Total || !first.Compensation.Estimated {
		t.Fatalf("salary units or source qualifiers changed: %+v", first.Compensation)
	}
	if !hidden.Compensation.Hidden || hidden.Compensation.From != nil || hidden.Compensation.To != nil || hidden.Compensation.Currency != nil {
		t.Fatalf("hidden compensation became a disclosed budget: %+v", hidden.Compensation)
	}
	if len(first.Locations) != 3 || !first.Locations[2].Exclude || !reflect.DeepEqual(first.KeySkills, []string{" Go ", "PostgreSQL"}) {
		t.Fatalf("location restrictions or actual tags lost: %+v", first)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE_SENTINEL") || strings.Contains(string(data), "profile_filters") || strings.Contains(string(data), "\"filters\"") {
		t.Fatalf("search leaked account data or a filter inventory: %s", data)
	}
}

func TestSearchVacanciesPageContract(t *testing.T) {
	for _, scenario := range []string{
		"all fields", "last page", "empty", "no matching tags", "unknown specialization",
		"missing meta", "wrong offset", "wrong limit", "missing offers", "partial page",
		"duplicate vacancy", "foreign URL", "wrong vacancy ID", "missing skill tags",
		"excluded remote only", "office only", "missing salary visibility",
	} {
		t.Run(scenario, func(t *testing.T) {
			options := domain.VacancySearchOptions{Remote: true, PerPage: 20}
			row := searchOfferFixture(101, []any{Object{"name": "Go"}})
			page := searchPageFixture([]any{row}, 1, 0, 20)
			wantErr := ""
			switch scenario {
			case "last page":
				options.Page = 1
				page["meta"] = Object{"total": 21, "offset": 20, "limit": 20}
			case "empty":
				page["offers"] = []any{}
				page["meta"].(Object)["total"] = 0
			case "no matching tags":
				options.Skills = []string{"Rust"}
			case "unknown specialization":
				options.Specializations, wantErr = []string{"not-a-specialization"}, "not-a-specialization"
			case "missing meta":
				delete(page, "meta")
				wantErr = "pagination"
			case "wrong offset":
				page["meta"].(Object)["offset"], wantErr = 20, "offset"
			case "wrong limit":
				page["meta"].(Object)["limit"], wantErr = 100, "limit"
			case "missing offers":
				delete(page, "offers")
				wantErr = "offers"
			case "partial page":
				page["meta"].(Object)["total"], wantErr = 25, "offer count"
			case "duplicate vacancy":
				page["offers"], page["meta"].(Object)["total"], wantErr = []any{row, row}, 2, "repeats vacancy 101"
			case "foreign URL":
				row["url"], wantErr = "https://example.org/vacancies/101-backend", "identity"
			case "wrong vacancy ID":
				row["url"], wantErr = "/vacancies/102-other", "identity"
			case "missing skill tags":
				delete(row, "skills_objects")
				wantErr = "skills_objects"
			case "excluded remote only":
				row["location_items"] = []any{Object{"label": "Россия", "format": "remote", "exclude": true}}
				wantErr = "included remote"
			case "office only":
				row["location_items"] = []any{Object{"label": "Москва", "format": "office", "exclude": false}}
				wantErr = "included remote"
			case "missing salary visibility":
				delete(row, "salary_hidden")
				wantErr = "salary"
			}
			calls := 0
			client := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				wantPage, wantOffset := "1", "0"
				if scenario == "last page" {
					wantPage, wantOffset = "2", "20"
				}
				q := r.URL.Query()
				if r.Method != http.MethodGet || q.Get("p") != wantPage || q.Get("offset") != wantOffset || q.Get("sa") != "any" || q.Get("pa") != "all" || q.Has("se") || scenario != "unknown specialization" && q.Has("sp") {
					t.Fatalf("paging or default filters changed: %s %s", r.Method, r.URL)
				}
				data, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}, nil)
			result, err := client.SearchVacancies(context.Background(), options)
			if calls != 1 {
				t.Fatalf("search retried or fetched extra pages: %d requests", calls)
			}
			if wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), wantErr) || result != nil {
					t.Fatalf("expected %q without partial output, got result=%+v err=%v", wantErr, result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if scenario == "empty" || scenario == "no matching tags" {
				wantCount = 0
			}
			if result.Vacancies == nil || len(result.Vacancies) != wantCount || result.NextPage != nil || result.Page != options.Page {
				t.Fatalf("last/empty page or local filtering changed: %+v", result)
			}
		})
	}
}
