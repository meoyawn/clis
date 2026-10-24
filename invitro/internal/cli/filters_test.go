package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestIncludeFilters(t *testing.T) {
	for _, tc := range []struct {
		name, kind, endpoint string
		empty, include       bool
	}{
		{name: "analyses", kind: "analyses", endpoint: "tests", include: true},
		{name: "empty", kind: "analyses", endpoint: "tests", empty: true, include: true},
		{name: "complexes", kind: "complexes", endpoint: "complexes", empty: true, include: true},
		{name: "checkups", kind: "checkups", endpoint: "checkups", empty: true, include: true},
		{name: "disabled", kind: "analyses", endpoint: "tests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tempPaths(t)
			if err := atomicJSON(p.config, userConfig{City: "kazan"}); err != nil {
				t.Fatal(err)
			}
			requests, filterRequests := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Query().Has("include-filters") || r.URL.Query().Has("include_filters") {
					t.Error("output flag sent to API")
				}
				switch r.URL.Path {
				case "/golk/addresses/api/v1/cities":
					writeFixture(t, w, "city")
				case "/golk/search/api/v1/search/" + tc.kind:
					if r.URL.Query().Get("q") != "ттг" || r.URL.Query().Get("cityId") != testCityID {
						t.Error("wrong query or city")
					}
					if tc.empty {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprintf(w, `{"group":%q,"ids":[]}`, tc.kind)
					} else {
						writeFixture(t, w, "search")
					}
				case "/golk/tests/api/v1/" + tc.endpoint + "/by-ids":
					if r.URL.Query().Get("is_promo") != "false" {
						t.Error("applied filter lost")
					}
					writeFixture(t, w, "page")
				case "/golk/tests/api/v1/" + tc.endpoint + "/filters":
					filterRequests++
					if r.Method != http.MethodGet || r.URL.RawQuery != "" {
						t.Error("filter metadata is an unparameterized GET")
					}
					if tc.kind == "checkups" {
						writeFixture(t, w, "checkup-filters")
					} else {
						writeFixture(t, w, "filters")
					}
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			// Exercise flags on both sides of the query and the saved city.
			args := []string{"search", "--type", tc.kind, "ттг", "--is-promo=false", "--include-filters=" + fmt.Sprint(tc.include)}
			if tc.include {
				args = []string{"search", "--include-filters", "ттг", "--type", tc.kind, "--is-promo=false"}
			}
			out, err := execute(t, p, server.URL, args...)
			if err != nil {
				t.Fatal(err)
			}
			var result searchResult
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatal(err)
			}
			expected := 3
			if tc.empty {
				expected--
			}
			if tc.include {
				expected++
			}
			if result.HTTPRequests != expected || requests != expected {
				t.Fatalf("request count %d/%d, want %d", result.HTTPRequests, requests, expected)
			}
			if !reflect.DeepEqual(result.Filters, map[string]string{"is_promo": "false"}) {
				t.Errorf("applied filters changed: %v", result.Filters)
			}
			if tc.empty && (result.Results == nil || result.Total != 0 || result.Count != 0) {
				t.Error("empty search changed")
			}
			if !tc.empty && (result.Total != 9 || result.Count != 9) {
				t.Error("search results changed")
			}
			if tc.include {
				if filterRequests != 1 || result.AvailableFilters == nil {
					t.Fatal("missing filter metadata")
				}
				catalog := *result.AvailableFilters
				want := 1
				if tc.kind != "checkups" {
					want = 3
				}
				if len(catalog) != want {
					t.Errorf("wrong filter count: %v", catalog)
				}
				promo := catalog["is_promo"]
				if promo.CLIFlag != "--is-promo" || promo.Label != "Акция" || promo.Type != "TOGGLE" || !promo.IsActive || promo.Config != "{}" {
					t.Errorf("wrong metadata: %+v", promo)
				}
				if tc.kind != "checkups" && (catalog["available_at_home"].CLIFlag != "--available-at-home" || catalog["deadline"].CLIFlag != "--deadline") {
					t.Error("missing filter flag names")
				}
			} else if filterRequests != 0 || result.AvailableFilters != nil || strings.Contains(out, `"available_filters"`) {
				t.Error("metadata fetched or output without flag")
			}
		})
	}
}

func TestIncludeFiltersFailureDoesNotWritePartialOutput(t *testing.T) {
	p := tempPaths(t)
	for _, body := range []string{`null`, `[]`, `{"deadline":null}`, `{"deadline":{"id":"bad","label":"x","type":"SELECT_ONE"}}`, `<html>challenge</html>`} {
		requests := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			switch r.URL.Path {
			case "/golk/addresses/api/v1/cities":
				writeFixture(t, w, "city")
			case "/golk/search/api/v1/search/analyses":
				writeFixture(t, w, "search")
			case "/golk/tests/api/v1/tests/by-ids":
				writeFixture(t, w, "page")
			case "/golk/tests/api/v1/tests/filters":
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			default:
				t.Errorf("unexpected request %s", r.URL)
				http.NotFound(w, r)
			}
		}))
		out, err := execute(t, p, server.URL, "search", "ттг", "--city", "kazan", "--include-filters")
		server.Close()
		if err == nil || out != "" || requests != 4 {
			t.Errorf("body=%s output=%q err=%v requests=%d", body, out, err, requests)
		}
	}
}

func TestEmptyFilterCatalogIsIncluded(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "cities") {
			writeFixture(t, w, "city")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "filters") {
			fmt.Fprint(w, `{}`)
		} else {
			fmt.Fprint(w, `{"group":"analyses","ids":[]}`)
		}
	})
	r, err := c.Search(context.Background(), "ттг", "kazan", Analyses(), 1, searchFilters{Deadline: -1}, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"available_filters":{}`) {
		t.Fatalf("missing empty metadata object: %s", data)
	}
	if c.requestLimit != 4 || r.HTTPRequests != 3 {
		t.Error("wrong extended budget")
	}
	c.requests = 4
	if err := c.request(context.Background(), "/golk/test", nil, nil, new(any)); err == nil {
		t.Error("extended budget exceeded")
	}
}
