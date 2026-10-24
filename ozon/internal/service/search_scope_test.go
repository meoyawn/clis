package service

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func putWidget(t *testing.T, p *page, id string, value object) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	p.WidgetStates[id] = b
}

func TestSearchExcludesOtherGridProducers(t *testing.T) {
	t.Parallel()
	p := fixturePage(t, "search")
	p.Layout = append(p.Layout, layoutWidget{Component: "tileGridDesktop", Name: "recommendations.products", StateID: "tileGridDesktop-1-default-1"})
	// This malformed recommendation must not even be parsed as a search result.
	p.WidgetStates["tileGridDesktop-1-default-1"] = json.RawMessage(`{"items":[{"sku":200,"unrelated":"shoes"}]}`)
	r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test")
	if err != nil || r.Count != 1 || r.Results[0].SKU != "100" || !r.HasMore {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	for _, layout := range [][]layoutWidget{
		nil,
		{{Component: "tileGridDesktop", Name: "recommendations.products", StateID: "tileGridDesktop-123-default-1"}},
		append(p.Layout, layoutWidget{Component: "tileGridDesktop", Name: "catalog.searchResultsV2", StateID: "tileGridDesktop-1-default-1"}),
	} {
		p.Layout = layout
		if r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test"); err == nil || r != nil {
			t.Fatal("unidentified or ambiguous result source was accepted")
		}
	}
}

func TestSearchStopsAtRecommendationContinuation(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"non_found", "sold_out_page", "search_tiles_req"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			p := fixturePage(t, "search")
			p.NextPage = "/search/?text=test&page=2&" + key + "=1"
			r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test")
			if err != nil || r.Count != 1 || r.HasMore || r.NextCursor != "" || r.NextPage != nil || r.Message == "" {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			p.PageInfo.URL = "/search/?text=test&" + key + "=1"
			if r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test"); err == nil || r != nil {
				t.Fatal("recommendation response accepted")
			}
		})
	}
}

func TestSearchRejectsChangedCriteriaInResponseAndCursor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ want, changed string }{
		{"text=test", "text=shoes"},
		{"text=test&delivery=4", "text=test"},
		{"text=test&delivery=4", "text=test&delivery=7"},
		{"text=test", "text=test&type=8"},
		{"text=test&currency_price=49%3B250", "text=test&currency_price=49%3B300"},
		{"text=test&sorting=price", "text=test&sorting=score"},
		{"text=test", "text=test&brand=100&brand_was_predicted=true"},
		{"text=test&brand=100&brand_was_predicted=true", "text=test&brand=200&brand_was_predicted=true"},
		{"text=test&brand=100", "text=test"},
	} {
		t.Run(tc.want+"->"+tc.changed, func(t *testing.T) {
			t.Parallel()
			p := fixturePage(t, "search")
			path := "/search/?" + tc.want
			p.PageInfo.URL = "/search/?" + tc.changed
			if _, err := validateSearchResponse(p, path); err == nil {
				t.Fatal("changed response accepted")
			}
			want, err := searchURL(path)
			if err != nil {
				t.Fatal(err)
			}
			next, err := searchURL("/search/?" + tc.changed + "&page=2")
			if err != nil {
				t.Fatal(err)
			}
			if err := sameSearchConstraints(want.Query(), next.Query()); err == nil {
				t.Fatal("changed continuation accepted")
			}
		})
	}
	p := fixturePage(t, "search")
	p.NextPage = "/search/?text=test&page=2&delivery=4"
	if r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test"); err == nil || r != nil {
		t.Fatal("search exposed a continuation that adds a filter")
	}
	p.NextPage = ""
	putWidget(t, p, "tapTags-1-default-1", object{"searchText": "corrected query"})
	if r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test"); err == nil || r != nil {
		t.Fatal("rewritten query accepted")
	}
}

func TestCategorySearchKeepsItsScope(t *testing.T) {
	t.Parallel()
	const path = "/category/coconut-oil-100/aroy-d-200/?text=test"
	for _, changed := range []string{
		"/search/?text=test",
		"/category/coconut-oil-100/?text=test",
		"/category/coconut-oil-100/another-brand-300/?text=test",
	} {
		p := fixturePage(t, "search")
		p.PageInfo.URL, p.PageInfo.PageType = path, "category"
		p.NextPage = changed + "&page=2"
		if _, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, path); err == nil {
			t.Fatalf("accepted changed category in continuation: %s", changed)
		}
		p.PageInfo.URL = changed
		if _, err := validateSearchResponse(p, path); err == nil {
			t.Fatalf("accepted changed category in response: %s", changed)
		}
	}
	p := fixturePage(t, "search")
	p.PageInfo.URL, p.PageInfo.PageType = path, "category"
	if _, err := validateSearchResponse(p, "/search/?text=test"); err == nil {
		t.Fatal("accepted a category response without a redirect")
	}
	p.PageInfo.PageType = "search"
	if _, err := validateSearchResponse(p, path); err == nil {
		t.Fatal("accepted the wrong category page type")
	}
}

func TestIgnoredFilterProducesNoResultsWithOneRequest(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"search", "filters"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				p := fixturePage(t, "search")
				p.PageInfo.URL = requireRequest(t, r)
				putWidget(t, p, "searchResultsFiltersActive-1-default-1", object{"activeFilters": []any{}})
				b, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(b, 200), nil
			})
			o := SearchOptions{Query: "test", Page: 1, Filters: url.Values{"typo_filter": {"1"}}}
			var err error
			if command == "search" {
				var r *SearchResult
				r, err = c.Search(t.Context(), o, RequestOptions{})
				if r != nil {
					t.Fatal("unfiltered results returned")
				}
			} else {
				_, err = c.Filters(t.Context(), o, RequestOptions{})
			}
			if err == nil || !strings.Contains(err.Error(), "typo_filter") || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestFilterConfirmationValuesAndNumericNormalization(t *testing.T) {
	t.Parallel()
	p := fixturePage(t, "search")
	putWidget(t, p, "searchResultsFiltersActive-1-default-1", object{"activeFilters": []any{
		object{"key": "currency_price", "ftype": "RESPONSE_FILTER_TYPE_RANGE", "activeValues": []any{object{"cellTrackingInfo": object{"filterValue": "49:250"}}}},
		object{"key": "type", "ftype": "RESPONSE_FILTER_TYPE_MULTI", "activeValues": []any{object{"cellTrackingInfo": object{"filterValue": "200"}}, object{"cellTrackingInfo": object{"filterValue": "100"}}}},
	}})
	q := url.Values{"text": {"test"}, "currency_price": {"49.000;250.000"}, "type": {"100;200"}}
	if err := validateActiveFilters(p, q); err != nil {
		t.Fatal(err)
	}
	if err := sameSearchConstraints(q, url.Values{"text": {"test"}, "currency_price": {"49;250"}, "type": {"100;200"}, "page": {"2"}, "paginator_token": {"new"}}); err != nil {
		t.Fatal(err)
	}
	q.Set("type", "100;300")
	if err := validateActiveFilters(p, q); err == nil {
		t.Fatal("different selected values accepted")
	}
	path, err := SearchPath(SearchOptions{Query: "true", Page: 1, Filters: url.Values{"currency_price": {"49.4300;250"}, "is_high_rating": {"true"}}})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("currency_price") != "49.430;250.000" || u.Query().Get("is_high_rating") != "t" || u.Query().Get("text") != "true" {
		t.Fatal("filter normalization changed the query or failed to use canonical values")
	}
	q = url.Values{"text": {"test"}, "sorting": {"price"}}
	if err := validateActiveFilters(p, q); err == nil {
		t.Fatal("unconfirmed sorting accepted")
	}
	putWidget(t, p, "searchResultsSort-1-default-1", object{"sortButton": object{"options": []any{object{"isSelected": true, "action": object{"link": "/search/?text=test&sorting=price"}}}}})
	if err := validateActiveFilters(p, q); err != nil {
		t.Fatal(err)
	}
}

func TestSearchExcludesOutOfRangeAndUnknownPrices(t *testing.T) {
	t.Parallel()
	p := fixturePage(t, "search")
	path := "/search/?text=test&currency_price=1000%3B1300"
	p.PageInfo.URL = path
	p.NextPage = path + "&page=2"
	putWidget(t, p, "searchResultsFiltersActive-1-default-1", object{"activeFilters": []any{
		object{"key": "currency_price", "ftype": "RESPONSE_FILTER_TYPE_RANGE", "activeValues": []any{object{"cellTrackingInfo": object{"filterValue": "1000:1300"}}}},
	}})
	g, err := p.first("tileGridDesktop")
	if err != nil {
		t.Fatal(err)
	}
	g["items"] = append(arr(g["items"]),
		object{"sku": "200", "mainState": []any{object{"type": "textDS", "id": "name", "textDS": object{"text": "Too cheap"}}, object{"type": "priceV2", "priceV2": object{"price": []any{object{"textStyle": "PRICE", "text": "999 ₽"}}}}}},
		object{"sku": "300", "mainState": []any{object{"type": "textDS", "id": "name", "textDS": object{"text": "No price"}}}},
		object{"sku": "400", "mainState": []any{object{"type": "textDS", "id": "name", "textDS": object{"text": "Too expensive"}}, object{"type": "priceV2", "priceV2": object{"price": []any{object{"textStyle": "PRICE", "text": "1301 ₽"}}}}}},
	)
	putWidget(t, p, "tileGridDesktop-123-default-1", g)
	r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, path)
	if err != nil || r.Count != 1 || r.Results[0].SKU != "100" || r.ExcludedCount != 3 || !r.HasMore || len(r.Warnings) != 1 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
}

func TestRecommendationAndMalformedCursorsMakeZeroRequests(t *testing.T) {
	t.Parallel()
	c := testClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("request made for a non-search cursor")
		return nil, nil
	})
	for _, query := range []string{
		"text=test&non_found=1&page=2", "text=test&sold_out_page=1&page=2", "text=test&search_tiles_req=opaque&page=2",
		"text=test&text=shoes&page=2", "text=test&page=2&page=3", "text=test&page=2&layout_page_index=3",
		"text=test&currency_price=49;250&page=2", "text=test&currency_price=250%3B49&page=2",
	} {
		if _, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, Cursor: "/search/?" + query}, RequestOptions{}); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	for _, filters := range []url.Values{{"non_found": {"1"}}, {"sold_out_page": {"1"}}, {"search_tiles_req": {"state"}}, {"deny_category_prediction": {"false"}}, {"delivery": {"4", "7"}}, {"currency_price": {"250;49"}}} {
		if _, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, Filters: filters}, RequestOptions{}); err == nil {
			t.Errorf("accepted filters %v", filters)
		}
	}
}
