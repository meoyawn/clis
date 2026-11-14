package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

// Synthetic records preserve the search wrapper and detail-page skill contract
// observed through Moli. Search snippets intentionally contain no skill tags.
func searchRow(id int, name string) hh.Object {
	return hh.Object{
		"vacancyId": id, "name": name,
		"company":         hh.Object{"id": 201, "name": "Example employer"},
		"area":            hh.Object{"@id": 1, "name": "Москва"},
		"workFormats":     []any{hh.Object{"workFormatsElement": []any{"REMOTE"}}},
		"compensation":    hh.Object{"from": 200000, "currencyCode": "RUR", "gross": false, "mode": "MONTH", "perModeFrom": 200000},
		"publicationTime": hh.Object{"$": "2026-11-14T10:00:00+03:00"},
	}
}

func searchPage(text string, rows []any, perPage int) hh.Object {
	return hh.Object{"vacancySearchResult": hh.Object{
		"criteria":     hh.Object{"text": text, "area": []any{}, "work_format": []any{"REMOTE"}, "page": 0, "items_on_page": perPage},
		"totalResults": 100, "vacancies": rows,
		"paging": hh.Object{"next": hh.Object{"page": 1, "disabled": false}},
	}}
}

func TestSearchMatchesActualGoOrGolangTags(t *testing.T) {
	rows := []any{
		searchRow(101, "Backend engineer"), searchRow(102, "Platform engineer"),
		searchRow(103, "Golang developer"), searchRow(104, "Go engineer"),
		searchRow(105, "Infrastructure engineer"), searchRow(106, "Previously remote"),
		searchRow(107, "Confidential employer role"),
	}
	anonymous := rows[6].(hh.Object)
	anonymous["company"] = hh.Object{"visibleName": "Confidential example employer"}
	anonymous["vacancyProperties"] = hh.Object{"calculatedStates": hh.Object{"HH": hh.Object{"anonymous": true}}}
	skills := map[string][]string{
		"101": {" Go ", "OpenTelemetry"}, "102": {"GOLANG"},
		"103": {"Rust"}, "104": {"GoCD", "Django"}, "105": {}, "106": {"Go"}, "107": {"Golang"},
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet {
			t.Errorf("search attempted a mutation: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected mutation", 500)
			return
		}
		if r.URL.Path == "/search/vacancy" {
			want := url.Values{"text": {`"Go" OR "Golang"`}, "work_format": {"REMOTE"}, "page": {"0"}, "items_on_page": {"20"}}
			if !reflect.DeepEqual(r.URL.Query(), want) {
				t.Errorf("wrong discovery query (must not inherit Kazan): %v", r.URL.Query())
			}
			state, _ := json.Marshal(searchPage(`"Go" OR "Golang"`, rows, 20))
			fmt.Fprintf(w, `<html><template id="HH-Lux-InitialState">%s</template></html>`, state)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/vacancy/")
		tags, exists := skills[id]
		if !exists {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		format := "REMOTE"
		if id == "106" {
			format = "ON_SITE"
		}
		vacancy := hh.Object{
			"vacancyId": id, "name": "Live title " + id,
			"company":     hh.Object{"id": 201, "name": "Example employer"},
			"description": "<p>Go and Golang in description do not establish key skills.</p>",
			"keySkills":   tags, "workFormats": []string{format}, "status": hh.Object{"archived": false},
			"compensation": hh.Object{"to": 300000, "currencyCode": "RUR", "gross": true, "mode": "MONTH"},
		}
		if id == "107" {
			vacancy["company"] = hh.Object{"visibleName": "Confidential example employer"}
			vacancy["vacancyProperties"] = hh.Object{"calculatedStates": hh.Object{"HH": hh.Object{"anonymous": true}}}
		}
		_ = json.NewEncoder(w).Encode(hh.Object{"vacancyView": hh.Object{"vacancyFull": hh.Object{"vacancy": vacancy}}})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	result, err := hh.NewClient(server.Client(), base).SearchVacancies(context.Background(), domain.VacancySearchOptions{
		Skills: []string{"Go", "Golang"}, Remote: true, PerPage: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCandidates != 100 || result.ScannedCandidates != 7 || result.NextPage == nil || *result.NextPage != 1 || result.Page != 0 {
		t.Fatalf("wrong candidate pagination: %+v", result)
	}
	if len(result.Vacancies) != 3 || result.Vacancies[0].ID != "101" || result.Vacancies[1].ID != "102" || result.Vacancies[2].ID != "107" {
		t.Fatalf("skill filter accepted prose, substrings or a stale remote flag: %+v", result.Vacancies)
	}
	first := result.Vacancies[0]
	if first.Title != "Live title 101" || first.Area.Name != "Москва" || !reflect.DeepEqual(first.KeySkills, []string{" Go ", "OpenTelemetry"}) || first.Compensation["to"] != float64(300000) {
		t.Fatalf("lost live skill, location or salary evidence: %+v", first)
	}
	if result.Vacancies[2].Employer.ID != "" || result.Vacancies[2].Employer.Name != "Confidential example employer" {
		t.Fatalf("anonymous employer identity was invented: %+v", result.Vacancies[2])
	}
	if len(paths) != 8 {
		t.Fatalf("expected one search and seven detail GETs, got %v", paths)
	}
}

func TestSearchNativeQueryAndCriteria(t *testing.T) {
	const text = `NAME:(Go OR Golang) AND DESCRIPTION:"backend platform" NOT intern prefix* "near words"~5 ^COMPANY_NAME:Example !id:(123)`
	for _, scenario := range []string{"query", "next-page", "explicit-areas", "empty", "wrong-area", "wrong-remote", "rewritten-text", "wrong-page", "wrong-size", "missing-list", "duplicate", "nonremote", "unmarked-anonymous", "bad-next"} {
		t.Run(scenario, func(t *testing.T) {
			options := domain.VacancySearchOptions{Text: text, Remote: true, PerPage: 20}
			if scenario == "explicit-areas" {
				options.Areas = []string{"1", "2"}
			}
			if scenario == "next-page" {
				options.Page = 1
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/search/vacancy" || r.URL.Query().Get("text") != text || r.URL.Query().Get("work_format") != "REMOTE" || r.URL.Query().Get("page") != strconv.Itoa(options.Page) || !reflect.DeepEqual(r.URL.Query()["area"], options.Areas) {
					t.Errorf("native query changed or unexpected request: %s %s", r.Method, r.URL)
				}
				row := searchRow(101, "Backend engineer")
				state := searchPage(text, []any{row}, 20)
				list := state["vacancySearchResult"].(hh.Object)
				criteria := list["criteria"].(hh.Object)
				switch scenario {
				case "next-page":
					criteria["page"] = 1
					list["paging"] = hh.Object{"next": hh.Object{"page": 2, "disabled": false}}
				case "explicit-areas":
					criteria["area"] = []any{"2", "1"}
				case "empty":
					list["vacancies"], list["totalResults"], list["paging"] = []any{}, 0, nil
				case "wrong-area":
					criteria["area"] = []any{"88"}
				case "wrong-remote":
					criteria["work_format"] = []any{}
				case "rewritten-text":
					criteria["text"] = "Go"
				case "wrong-page":
					criteria["page"] = 1
				case "wrong-size":
					criteria["items_on_page"] = 50
				case "missing-list":
					delete(list, "vacancies")
				case "duplicate":
					list["vacancies"] = []any{row, row}
				case "nonremote":
					row["workFormats"] = []any{hh.Object{"workFormatsElement": []any{"ON_SITE"}}}
				case "unmarked-anonymous":
					row["company"] = hh.Object{"visibleName": "Confidential example employer"}
				case "bad-next":
					list["paging"] = hh.Object{"next": hh.Object{"page": 0, "disabled": false}}
				}
				_ = json.NewEncoder(w).Encode(state)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			result, err := hh.NewClient(server.Client(), base).SearchVacancies(context.Background(), options)
			if requests != 1 {
				t.Fatalf("query without skill filters must use one GET, got %d", requests)
			}
			if scenario != "query" && scenario != "next-page" && scenario != "explicit-areas" && scenario != "empty" {
				if err == nil || result != nil {
					t.Fatalf("unverified search accepted: %+v, %v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "empty" {
				if result.Vacancies == nil || len(result.Vacancies) != 0 || result.TotalCandidates != 0 || result.NextPage != nil {
					t.Fatalf("invalid empty page: %+v", result)
				}
				return
			}
			if len(result.Vacancies) != 1 || result.Vacancies[0].Compensation["from"] != float64(200000) || len(result.Vacancies[0].Compensation) != 4 {
				t.Fatalf("wrong compact search data: %+v", result)
			}
			if scenario == "next-page" && (result.Page != 1 || result.NextPage == nil || *result.NextPage != 2) {
				t.Fatalf("wrong zero-based continuation: %+v", result)
			}
		})
	}
}

func TestSearchStopsOnUnverifiableSkills(t *testing.T) {
	for _, scenario := range []string{"rate-limit", "missing-skills", "malformed-skills", "wrong-vacancy"} {
		t.Run(scenario, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path == "/search/vacancy" {
					_ = json.NewEncoder(w).Encode(searchPage(`"Go"`, []any{searchRow(101, "Backend"), searchRow(102, "Backend")}, 20))
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/vacancy/101" {
					t.Errorf("continued after failure or attempted mutation: %s %s", r.Method, r.URL)
				}
				if scenario == "rate-limit" {
					w.Header().Set("Retry-After", "60")
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"error":"rate_limited"}`)
					return
				}
				vacancy := hh.Object{"vacancyId": 101, "name": "Backend", "description": "<p>Go work</p>", "company": hh.Object{"id": 201, "name": "Example employer"}}
				if scenario == "malformed-skills" {
					vacancy["keySkills"] = []any{1}
				}
				if scenario == "wrong-vacancy" {
					vacancy["vacancyId"] = 102
				}
				_ = json.NewEncoder(w).Encode(hh.Object{"vacancyView": hh.Object{"vacancyFull": hh.Object{"vacancy": vacancy}}})
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			result, err := hh.NewClient(server.Client(), base).SearchVacancies(context.Background(), domain.VacancySearchOptions{Skills: []string{"Go"}, Remote: true, PerPage: 20})
			if err == nil || result != nil || requests != 2 {
				t.Fatalf("failure hidden, partial result emitted or reads continued: requests=%d result=%+v err=%v", requests, result, err)
			}
			if scenario == "rate-limit" {
				var diagnostic *hh.HTTPError
				if !errors.As(err, &diagnostic) || diagnostic.Status != 429 || diagnostic.RetryAfter != "60" {
					t.Fatalf("lost rate-limit diagnostic: %v", err)
				}
			}
		})
	}
}
