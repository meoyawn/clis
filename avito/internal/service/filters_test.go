package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func fridgeOptions() SearchOptions {
	return SearchOptions{Query: "холодильник", LocationID: 650400, CategoryID: 21, LocalPriority: true, Page: 1,
		Filters: url.Values{"params[48]": {"5067"}, "params[486]": {"5072"}, "params[172087]": {"3281454"},
			"params[110369]": {"431047"}, "params[145393]": {"18204028"}, "pmax": {"5000"}, "owner": {"1"}}}
}

func testSort(t *testing.T, value string) Sort {
	t.Helper()
	sorting, err := NewSortFromValue(value)
	if err != nil {
		t.Fatal(err)
	}
	return sorting
}

func TestFilterDiscoveryPreservesHierarchyAndDependentInputs(t *testing.T) {
	t.Parallel()
	result, err := ParseFilters(fixture(t, "web_filters.json"), fridgeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CategoryPath) != 3 || len(result.Categories) != 2 || result.CategoryPath[2].Name != "Холодильники" || result.Categories[1].Filters.Get("params[172087]") != "3281455" {
		t.Fatalf("lost category navigation: %+v", result.Categories)
	}
	byKey := map[string]Filter{}
	for _, filter := range result.Filters {
		byKey[filter.Key] = filter
	}
	brand := byKey["params[145393]"]
	if !brand.Multiple || !brand.HasSuggest || !brand.UpdatesFilters || !brand.Options[0].Selected || brand.Options[0].Value != "18204028" {
		t.Fatalf("brand is not usable: %+v", brand)
	}
	if byKey["params[110369]"].Multiple || byKey["params[145394]"].Options[0].Label != "Example Model" || byKey["params[196498]"].Group != "Продавцы" {
		t.Fatal("lost radio selection, sectioned options, or nested filters")
	}
	price := byKey["price"]
	if !price.Supported || price.Inputs[1].Key != "pmax" || price.Inputs[1].Selected != "5000" || byKey["params[145400]"].Inputs[0].Key != "params[145400][from]" {
		t.Fatalf("lost price/dimension range input keys: %+v", price)
	}
	if byKey["params[149569]"].Type != "keywords" || !byKey["params[149569]"].Supported || result.Sorting[1].Value != "1" || !result.Sorting[1].Selected {
		t.Fatal("lost keyword or sorting discovery")
	}
	for _, arg := range []string{"params[48]=5067", "params[486]=5072", "params[172087]=3281454", "params[145393]=18204028", "pmax=5000"} {
		if !slices.Contains(result.SearchArgs, arg) {
			t.Fatalf("search_args lost selection %q: %v", arg, result.SearchArgs)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "SYNTHETIC_FILTER_SECRET") {
		t.Fatal("filter output leaked private response state")
	}
}

func TestFilteredSearchExcludesUnusablePriceComparables(t *testing.T) {
	t.Parallel()
	result, err := ParseSearch(fixture(t, "web_filters.json"), fridgeOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 || result.ExcludedCount != 2 || result.Total != 4 || len(result.Warnings) != 1 || result.HasMore == nil || *result.HasMore {
		t.Fatalf("wrong filtered sample: %+v", result)
	}
	if result.Results[0].Price.Amount == nil || *result.Results[0].Price.Amount != 2000 || !strings.Contains(result.Results[0].Description, "10 лет") {
		t.Fatal("lost price or condition evidence")
	}
}

func TestRecommendationsCannotExtendSearchPagination(t *testing.T) {
	t.Parallel()
	body := string(fixture(t, "web_filters.json"))
	body = strings.Replace(body, `"totalCount": 4`, `"totalCount": 1096, "extraCount": 1092`, 1)
	result, err := ParseSearch([]byte(body), fridgeOptions())
	if err != nil || result.AccessibleTotal == nil || *result.AccessibleTotal != 4 || result.SimilarTotal == nil || *result.SimilarTotal != 1092 || result.HasMore == nil || *result.HasMore || result.NextPage != nil {
		t.Fatalf("pagination escaped into recommendations: %+v, %v", result, err)
	}
}

func TestSearchAndFiltersRejectUnappliedSelections(t *testing.T) {
	t.Parallel()
	for _, change := range []func(*SearchOptions){
		func(o *SearchOptions) { o.Filters.Set("params[145393]", "999") },
		func(o *SearchOptions) { o.Filters.Set("params[999]", "1") },
		func(o *SearchOptions) { o.Filters.Add("params[145393]", "999") },
		func(o *SearchOptions) { o.Filters.Set("pmax", "4999") },
		func(o *SearchOptions) { o.Filters.Set("owner", "2") },
		func(o *SearchOptions) { o.Filters.Set("params[149569]", "не холодит") },
		func(o *SearchOptions) { o.CategoryID = 32 },
		func(o *SearchOptions) { o.Sort = testSort(t, "104") },
	} {
		o := fridgeOptions()
		change(&o)
		if _, err := ParseSearch(fixture(t, "web_filters.json"), o); err == nil {
			t.Fatalf("search accepted an ignored selection: %+v", o)
		}
		if _, err := ParseFilters(fixture(t, "web_filters.json"), o); err == nil {
			t.Fatalf("filters accepted an ignored selection: %+v", o)
		}
	}
}

func TestFilterEncodingRangesKeywordsAndPagination(t *testing.T) {
	t.Parallel()
	o := fridgeOptions()
	o.Page = 2
	o.Sort = testSort(t, "104")
	o.Filters["params[145393]"] = []string{"18204028", "900001"}
	o.Filters.Set("params[149569]", "не холодит & возраст=10")
	o.Filters.Set("params[145400][from]", "150.50")
	o.Filters.Set("params[145400][to]", "200")
	o.Filters.Set("byTitle", "1")
	o.Filters.Set("withImagesOnly", "0")
	o.Filters.Set("withDeliveryOnly", "1")
	target, err := SearchURL(o)
	if err != nil {
		t.Fatal(err)
	}
	q := target.Query()
	for key, want := range map[string]string{"p": "2", "s": "104", "user": "1", "pmax": "5000", "bt": "on", "i": "off", "d": "on", "params[145393][0]": "18204028", "params[145393][1]": "900001", "params[149569]": "не холодит & возраст=10", "params[145400][from]": "150.50"} {
		if q.Get(key) != want {
			t.Fatalf("request %s: %s=%q, want %q", target, key, q.Get(key), want)
		}
	}
	if q.Has("page") || q.Has("owner") || q.Has("возраст") {
		t.Fatal("incorrect transport key or query escaping")
	}
	core := webSearchCore{Query: o.Query, LocationID: o.LocationID, CategoryID: o.CategoryID, Page: 2, Sort: "104", Owner: json.RawMessage(`1`), PriceMax: json.RawMessage(`5000`),
		ByTitle: json.RawMessage(`1`), WithDeliveryOnly: json.RawMessage(`true`),
		Params: map[string]json.RawMessage{"48": json.RawMessage(`5067`), "486": json.RawMessage(`5072`), "172087": json.RawMessage(`3281454`), "110369": json.RawMessage(`431047`), "145393": json.RawMessage(`[900001,18204028]`), "149569": json.RawMessage(`"не холодит & возраст=10"`), "145400": json.RawMessage(`{"from":150.5,"to":200}`)}}
	selection, err := core.context(o)
	if err != nil || selection.AppliedFilters.Get("params[145400][from]") != "150.5" {
		t.Fatalf("could not verify scalar/array, range, or keyword selection: %+v, %v", selection, err)
	}
}

func TestInvalidFiltersFailBeforeRequests(t *testing.T) {
	t.Parallel()
	calls := 0
	client, err := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected request")
	})}, AuthStore{})
	if err != nil {
		t.Fatal(err)
	}
	for _, filters := range []url.Values{
		{"name": {"rewrite"}}, {"categoryId": {"21"}}, {"params[1][bad]": {"1"}}, {"params[0]": {"1"}},
		{"params[1]": {""}}, {"params[1]": {"x\n"}}, {"params[1]": {"1", "1"}},
		{"pmax": {"-1"}}, {"pmax": {"NaN"}}, {"pmin": {"200"}, "pmax": {"100"}},
		{"params[1]": {"1"}, "params[1][from]": {"2"}}, {"params[1][from]": {"9"}, "params[1][to]": {"2"}},
		{"owner": {"3"}}, {"pmax": {"1", "2"}},
	} {
		o := fridgeOptions()
		o.Filters = filters
		if _, err := client.Search(t.Context(), o, RequestOptions{}); err == nil || strings.Contains(err.Error(), "auth") {
			t.Fatalf("search did not reject filters before auth: %v, %v", filters, err)
		}
		if _, err := client.Filters(t.Context(), o, RequestOptions{}); err == nil || strings.Contains(err.Error(), "auth") {
			t.Fatalf("filters did not reject inputs before auth: %v, %v", filters, err)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs made %d requests", calls)
	}
}

func TestFilterValuesAreOneExplicitPage(t *testing.T) {
	t.Parallel()
	o := FilterValuesOptions{Search: fridgeOptions(), Key: "params[145393]", ValueQuery: "bek & x=1"}
	target, err := FilterValuesURL(o)
	if err != nil {
		t.Fatal(err)
	}
	if target.Path != filterSuggestPath || target.Query().Get("paramId") != "145393" || target.Query().Get("query") != o.ValueQuery || target.Query().Get("params[48]") != "5067" || target.Query().Has("name") {
		t.Fatalf("wrong option lookup: %s", target)
	}
	calls := 0
	client, err := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(fixture(t, "web_filter_values.json")))), Request: req}, nil
	})}, testAuthStore(t))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.FilterValues(t.Context(), o, RequestOptions{})
	if err != nil || calls != 1 || result.HTTPRequests != 1 || result.Count != 2 || result.Total != 3 || result.NextOffset == nil || *result.NextOffset != 2 || !result.HasMore || !result.Options[0].Selected {
		t.Fatalf("wrong filter value pagination/deduplication: %+v, %v, calls=%d", result, err, calls)
	}
	for _, body := range []string{`{}`, `null`, `{"status":"ok","result":{"optionsTotal":2}}`, `{"status":"error"}`} {
		if _, err := ParseFilterValues([]byte(body), o); err == nil {
			t.Fatalf("accepted invalid values response: %s", body)
		}
	}
	empty, err := ParseFilterValues([]byte(`{"status":"ok","result":{"optionsTotal":0,"nextOptionsOffset":0}}`), o)
	if err != nil || empty.Count != 0 || empty.HasMore {
		t.Fatalf("failed empty option lookup: %+v %v", empty, err)
	}
}

func TestSellerRatingAndPurchaseFiltersRoundTrip(t *testing.T) {
	t.Parallel()
	options := SearchOptions{Query: "скейтборд", LocationID: 650400, CategoryID: 39, Page: 1,
		Filters: url.Values{"params[196498]": {"3370585"}, "withDeliveryOnly": {"1"}, "params[186205]": {"1"}, "params[149578]": {"1"}}}
	result, err := ParseFilters(fixture(t, "web_seller_filters.json"), options)
	if err != nil {
		t.Fatal(err)
	}
	for key, values := range options.Filters {
		found := false
		for _, filter := range result.Filters {
			if filter.Key == key && filter.Supported && filter.Flag == "--filter" && len(filter.Options) == 1 && filter.Options[0].Selected && filter.Options[0].Value == values[0] {
				found = true
			}
		}
		if !found || !slices.Contains(result.SearchArgs, key+"="+values[0]) {
			t.Fatalf("filter %q was not exposed with a reusable selection: %+v", key, result)
		}
	}
	target, err := SearchURL(options)
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	for key, value := range map[string]string{"params[196498]": "3370585", "d": "on", "params[186205]": "1", "params[149578]": "1"} {
		if query.Get(key) != value {
			t.Fatalf("request lost %s=%s: %s", key, value, target)
		}
	}
	options.Filters.Set("params[149578]", "0")
	if _, err := ParseFilters(fixture(t, "web_seller_filters.json"), options); err == nil {
		t.Fatal("silently accepted a returns filter that the server did not apply")
	}
}
