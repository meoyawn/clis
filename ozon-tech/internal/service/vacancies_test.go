package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/meoyawn/clis/ozon-tech/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}
}

func syntheticVacancy(id int) domain.Vacancy {
	return domain.Vacancy{
		ID: strconv.Itoa(id), Slug: fmt.Sprintf("role-%d", id), Title: fmt.Sprintf("Go role %d", id),
		InternalUUID: fmt.Sprintf("%08x-0000-4000-8000-%012x", id, id), WorkFormats: []string{"Удалённая работа"},
	}
}

func TestCatalogWalksEveryPageWithUnchangedFilters(t *testing.T) {
	filters := domain.Filters{Directions: []string{"Разработка"}, WorkFormats: []string{"Удалённая работа"}}
	for _, total := range []int{0, 20, 43, 156} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			pages := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				pages++
				if request.Method != http.MethodPost || request.URL.String() != Origin+apiPath+"list" {
					t.Fatalf("unexpected request %s %s", request.Method, request.URL)
				}
				var payload struct {
					Filters   domain.Filters `json:"filters"`
					Page      int            `json:"page"`
					PageLimit int            `json:"page_limit"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.Page != pages || payload.PageLimit != 20 || !reflect.DeepEqual(payload.Filters, filters) {
					t.Fatalf("pagination/filter payload = %+v on request %d", payload, pages)
				}
				if pages > 8 {
					t.Fatal("pagination did not terminate")
				}
				items := []domain.Vacancy{}
				for id := (pages-1)*20 + 1; id <= min(pages*20, total); id++ {
					item := syntheticVacancy(id)
					if id == 1 {
						item.WorkFormats = []string{}
					}
					items = append(items, item)
				}
				return jsonResponse(t, map[string]any{"items": items, "total": strconv.Itoa(total)}), nil
			})}
			catalog, err := NewClient(httpClient).Catalog(t.Context(), filters, false, 4)
			if err != nil {
				t.Fatal(err)
			}
			if catalog.Count != total || len(catalog.Vacancies) != total || catalog.PagesFetched != max(1, (total+19)/20) || catalog.PagesFetched != pages {
				t.Fatalf("incomplete catalog: count %d, rows %d, pages %d/%d", catalog.Count, len(catalog.Vacancies), catalog.PagesFetched, pages)
			}
			for i, item := range catalog.Vacancies {
				if item.ID != strconv.Itoa(i+1) || !strings.Contains(item.URL, item.InternalUUID+"-"+item.Slug) {
					t.Fatalf("lost order/identity for item %d: %+v", i, item)
				}
			}
			if total > 0 && len(catalog.Vacancies[0].WorkFormats) != 0 {
				t.Fatal("invented a work format for the unlabelled source row")
			}
		})
	}
}

func TestCatalogRejectsIncompleteOrRepeatedPages(t *testing.T) {
	for _, mode := range []string{"short-page", "changed-total", "duplicate-id", "duplicate-uuid", "missing-total", "missing-items", "antibot"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if requests > 2 {
					t.Fatal("continued after a bad page")
				}
				items := []domain.Vacancy{}
				for id := (requests-1)*20 + 1; id <= min(requests*20, 23); id++ {
					items = append(items, syntheticVacancy(id))
				}
				result := map[string]any{"items": items, "total": "23"}
				if requests == 2 {
					switch mode {
					case "short-page":
						result["items"] = items[:2]
					case "changed-total":
						result["total"] = "24"
					case "duplicate-id":
						items[0].ID = "1"
					case "duplicate-uuid":
						items[0].InternalUUID = syntheticVacancy(1).InternalUUID
					case "missing-total":
						delete(result, "total")
					case "missing-items":
						delete(result, "items")
					case "antibot":
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>Antibot Challenge Page</title>"))}, nil
					}
				}
				return jsonResponse(t, result), nil
			})}
			catalog, err := NewClient(httpClient).Catalog(t.Context(), domain.Filters{Directions: []string{"Разработка"}, WorkFormats: []string{"Удалённая работа"}}, false, 4)
			if err == nil || catalog.Vacancies != nil || requests != 2 {
				t.Fatalf("bad page returned a catalog: err=%v, rows=%d, requests=%d", err, len(catalog.Vacancies), requests)
			}
		})
	}
}

func TestCatalogDetailsPreserveContentAndFailOnMissingCard(t *testing.T) {
	for _, mode := range []string{"complete", "missing", "wrong-identity", "unsupported-description"} {
		t.Run(mode, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == apiPath+"list" {
					return jsonResponse(t, map[string]any{"items": []domain.Vacancy{syntheticVacancy(1), syntheticVacancy(2)}, "total": 2}), nil
				}
				id, err := strconv.Atoi(strings.TrimPrefix(request.URL.Path, apiPath))
				if err != nil || request.Method != http.MethodGet {
					t.Fatalf("unexpected detail request %s %s", request.Method, request.URL)
				}
				if id == 2 && mode == "missing" {
					response := jsonResponse(t, map[string]any{})
					response.StatusCode = 404
					return response, nil
				}
				item := syntheticVacancy(id)
				item.Description = &domain.Description{HTML: `<h2>Задачи &amp; стек</h2><p>Разрабатывать <strong>MCP</strong>.</p><ul><li>Go</li><li>gRPC</li></ul><p><a href="/career/">Команда</a></p>`}
				if id == 2 && mode == "wrong-identity" {
					item.InternalUUID = syntheticVacancy(3).InternalUUID
				}
				if id == 2 && mode == "unsupported-description" {
					item.Description.Blocks = json.RawMessage(`[{"title":"must not be dropped"}]`)
				}
				return jsonResponse(t, item), nil
			})}
			catalog, err := NewClient(httpClient).Catalog(t.Context(), domain.Filters{Directions: []string{"Разработка"}, WorkFormats: []string{"Удалённая работа"}}, true, 2)
			if mode != "complete" {
				if err == nil || catalog.Vacancies != nil {
					t.Fatalf("failed description returned a catalog: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, item := range catalog.Vacancies {
				if item.ID != strconv.Itoa(i+1) || item.DetailsCheckedAt == nil {
					t.Fatalf("detail identity/date missing: %+v", item)
				}
				for _, part := range []string{"## Задачи & стек", "Разрабатывать **MCP**.", "- Go", "- gRPC", "[Команда](<https://ozon.tech/career/>)"} {
					if !strings.Contains(item.DescriptionMarkdown, part) {
						t.Fatalf("description lost %q: %s", part, item.DescriptionMarkdown)
					}
				}
			}
		})
	}
}

func TestRedirectsPreservePostAndRejectForeignDestinations(t *testing.T) {
	for _, location := range []string{Origin + apiPath + "list?__rr=1", "https://foreign.test/steal", "http://ozon.tech/p-api/ozon-tech/vacancy/list"} {
		t.Run(location, func(t *testing.T) {
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if requests == 1 {
					return &http.Response{StatusCode: 307, Header: http.Header{"Location": {location}}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
				}
				if requests > 2 || request.Method != http.MethodPost || request.URL.Host != "ozon.tech" || request.URL.Query().Get("__rr") != "1" {
					t.Fatalf("unsafe redirect %s %s", request.Method, request.URL)
				}
				b, err := io.ReadAll(request.Body)
				if err != nil || !strings.Contains(string(b), `"page_limit":1`) {
					t.Fatal("redirect lost its request body")
				}
				return jsonResponse(t, map[string]any{"total": 0, "items": []any{}}), nil
			})}
			err := NewClient(httpClient).CheckAccess(t.Context())
			allowed := strings.HasPrefix(location, Origin+"/")
			if allowed && (err != nil || requests != 2) || !allowed && (err == nil || requests != 1) {
				t.Fatalf("redirect outcome err=%v requests=%d", err, requests)
			}
		})
	}
}
