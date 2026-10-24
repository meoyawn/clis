package service

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSearchIncludesFiltersFromSamePage(t *testing.T) {
	t.Parallel()
	for _, include := range []bool{false, true} {
		t.Run(map[bool]string{false: "products only", true: "with facets"}[include], func(t *testing.T) {
			t.Parallel()
			p := fixturePage(t, "search")
			for id, widget := range fixturePage(t, "filters").WidgetStates {
				p.WidgetStates[id] = widget
			}
			want, err := parseFilters(p, "test")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				p.PageInfo.URL = requireRequest(t, r)
				body, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(body, http.StatusOK), nil
			})
			got, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, IncludeFilters: include}, RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.HTTPRequests != 1 || got.Count != 1 || !got.HasMore {
				t.Fatalf("requests=%d result=%+v", calls, got)
			}
			if include {
				if got.SearchFacets == nil || !reflect.DeepEqual(got.Filters, want.Filters) || !reflect.DeepEqual(got.Sorting, want.Sorting) {
					t.Fatal("combined search did not preserve standalone filter output")
				}
			} else if got.SearchFacets != nil {
				t.Fatal("facets returned without requesting them")
			}
			body, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"filters", "sorting"} {
				if _, present := fields[key]; present != include {
					t.Fatalf("JSON field %q inclusion=%v, want=%v", key, present, include)
				}
			}
		})
	}
}

func TestSearchIncludedFacetsUnavailableOrEmpty(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		t.Run(map[bool]string{false: "incremental page", true: "no matches"}[empty], func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				p := fixturePage(t, "search")
				p.PageInfo.URL, p.NextPage = requireRequest(t, r), ""
				if empty {
					putWidget(t, p, "statusWidget-1-default-1", object{"titleAtom": object{"text": "Nothing found", "testInfo": object{"automatizationId": "not-found-title"}}})
				} else {
					grid, err := p.first("tileGridDesktop")
					if err != nil {
						t.Fatal(err)
					}
					grid["page"] = 2
					putWidget(t, p, "tileGridDesktop-123-default-1", grid)
					p.Layout = []layoutWidget{{Component: "tileGridDesktop", Name: "catalog.searchResultsV2", StateID: "tileGridDesktop-123-default-1"}}
				}
				body, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(body, http.StatusOK), nil
			})
			o := SearchOptions{Query: "test", Page: 1, IncludeFilters: true}
			if !empty {
				o.Cursor = "/search/?text=test&page=2&paginator_token=opaque"
			}
			got, err := c.Search(t.Context(), o, RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.HTTPRequests != 1 || got.SearchFacets == nil {
				t.Fatalf("unexpected fetch or missing inclusion state: %+v", got)
			}
			if empty {
				if got.Count != 0 || got.Filters == nil || got.Sorting == nil || len(got.Warnings) != 0 {
					t.Fatalf("empty search should have empty facet arrays: %+v", got)
				}
			} else if got.Count != 1 || got.Filters != nil || got.Sorting != nil || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "filter facets") {
				t.Fatalf("unavailable facets should be null with a warning: %+v", got)
			}
		})
	}
}

func TestSearchIncludedFacetsRejectsMalformedWidget(t *testing.T) {
	t.Parallel()
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		p := fixturePage(t, "search")
		p.PageInfo.URL = requireRequest(t, r)
		putWidget(t, p, "filtersDesktop-1-default-1", object{"sections": "unexpected schema"})
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return response(body, http.StatusOK), nil
	})
	got, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, IncludeFilters: true}, RequestOptions{})
	if got != nil || err == nil || !strings.Contains(err.Error(), "sections") {
		t.Fatalf("malformed facets accepted: result=%+v err=%v", got, err)
	}
}
