package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testCityID = "19a36f51-f8d6-4ee5-8b2c-fef07ce8b5ff"
const testProductID = "ce302550-a6c3-4525-9bcf-c48939edafa4"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(fixture(t, name)); err != nil {
		t.Error(err)
	}
}

func testClient(t *testing.T, handler http.HandlerFunc) *client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c, err := newClient(server.URL, defaultUserAgent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.http.CloseIdleConnections)
	return c
}

func TestSearchRecordedPublicAPI(t *testing.T) {
	requests := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Web") != "true" || r.Header.Get("User-Agent") != defaultUserAgent {
			t.Error("missing public API headers")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("anonymous request included credentials")
		}
		switch r.URL.Path {
		case "/golk/addresses/api/v1/cities":
			if r.URL.Query().Get("q") != "kazan" {
				t.Error("wrong city query")
			}
			writeFixture(t, w, "city")
		case "/golk/search/api/v1/search/analyses":
			if r.URL.Query().Get("q") != "ттг & TSH" || r.URL.Query().Get("cityId") != testCityID {
				t.Error("wrong search query")
			}
			writeFixture(t, w, "search")
		case "/golk/tests/api/v1/tests/by-ids":
			if r.Method != http.MethodPost {
				t.Error("expected POST")
			}
			q := r.URL.Query()
			if q.Get("cityID") != testCityID || q.Get("limit") != "20" || q.Get("offset") != "0" || q.Get("available_at_home") != "false" || q.Get("is_promo") != "true" || q.Get("deadline") != "0" {
				t.Errorf("wrong filters: %v", q)
			}
			var body struct {
				IDs []string `json:"ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.IDs) != 9 || body.IDs[0] != testProductID {
				t.Error("wrong search IDs")
			}
			writeFixture(t, w, "page")
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := c.Search(context.Background(), "ттг & TSH", "kazan", Analyses(), 1, searchFilters{AtHome: optionalBool{set: true}, Promo: optionalBool{value: true, set: true}, Deadline: 0}, false)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || result.HTTPRequests != 3 || result.Total != 9 || result.Count != 9 || result.HasMore || result.NextPage != nil {
		t.Fatalf("bad pagination: %+v", result)
	}
	p := result.Results[0]
	if p.ID != "2467" || p.CatalogID != testProductID || p.PriceFrom == nil || *p.PriceFrom != 1955 || len(p.AdditionalServices) != 1 || *p.AdditionalServices[0].Price != 210 {
		t.Fatalf("incorrect product: %+v", p)
	}
	if p.URL != "https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/" || strings.Contains(p.Description, "<b>") {
		t.Errorf("bad URL or text: %+v", p)
	}
}

func TestPaginationAndFilteredTotals(t *testing.T) {
	for _, page := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(page), func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/golk/addresses/api/v1/cities":
					writeFixture(t, w, "city")
				case "/golk/search/api/v1/search/analyses":
					ids := []string{}
					for i := 1; i <= 25; i++ {
						ids = append(ids, fmt.Sprintf("00000000-0000-0000-0000-%012d", i))
					}
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]any{"group": "analyses", "ids": ids}); err != nil {
						t.Errorf("encode fixture response: %v", err)
					}
				default:
					var raw apiProduct
					if err := json.Unmarshal(fixture(t, "pageItem"), &raw); err != nil {
						t.Errorf("decode fixture: %v", err)
						return
					}
					offset := (page - 1) * pageSize
					if r.URL.Query().Get("offset") != fmt.Sprint(offset) {
						t.Error("wrong offset")
					}
					data := []apiProduct{}
					for i := offset + 1; i <= min(offset+20, 21); i++ {
						p := raw
						p.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
						p.BitrixID = int64(i)
						data = append(data, p)
					}
					w.Header().Set("Content-Type", "application/json")
					if err := json.NewEncoder(w).Encode(map[string]any{"total": 21, "limit": 20, "offset": offset, "data": data}); err != nil {
						t.Errorf("encode fixture response: %v", err)
					}
				}
			})
			result, err := c.Search(context.Background(), "test", "kazan", Analyses(), page, searchFilters{Deadline: 3}, false)
			if page == 3 {
				if err == nil || !strings.Contains(err.Error(), "exceeds") {
					t.Fatalf("expected page error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Total != 21 || result.TotalPages != 2 || result.Count != min(20, 21-(page-1)*20) {
				t.Fatalf("bad totals: %+v", result)
			}
			if page == 1 && (result.NextPage == nil || *result.NextPage != 2 || !strings.Contains(result.NextURL, "deadline=3") || !strings.Contains(result.NextURL, "page=2")) {
				t.Error("missing next page")
			}
			if page == 2 && (result.HasMore || result.NextPage != nil) {
				t.Error("last page has more")
			}
		})
	}
}

func TestEmptySearchSkipsProductRequest(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "cities") {
			writeFixture(t, w, "city")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"group":"analyses","ids":[]}`)
	})
	result, err := c.Search(context.Background(), "no-match", "kazan", Analyses(), 1, searchFilters{Deadline: -1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 0 || result.Count != 0 || result.Results == nil || result.HTTPRequests != 2 || result.TotalPages != 1 || result.NextPage != nil {
		t.Fatalf("bad empty result: %+v", result)
	}
}

func TestShowIdentityAndFullOutput(t *testing.T) {
	for _, full := range []bool{false, true} {
		for _, id := range []string{"2467", testProductID} {
			t.Run(fmt.Sprint(full, "/", id), func(t *testing.T) {
				c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/golk/addresses/api/v1/cities":
						writeFixture(t, w, "city")
					case "/golk/tests/api/v1/mappers/product/2467":
						writeFixture(t, w, "mapper")
					case "/golk/tests/api/v1/tests/" + testProductID:
						writeFixture(t, w, "detail")
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
					}
				})
				result, err := c.Show(context.Background(), productRef{ID: id}, "kazan", Analyses(), full)
				if err != nil {
					t.Fatal(err)
				}
				if result.Material != "Сыворотка крови" || result.Method == "" {
					t.Error("missing comparison facts")
				}
				if full && (len(result.Source) == 0 || result.Sections["preparation"] == "" || result.Sections["interpretation"] == "") {
					t.Error("missing full sections")
				}
				if !full && (result.Source != nil || result.Sections != nil) {
					t.Error("full output leaked into default")
				}
				expected := 2
				if id == "2467" {
					expected = 3
				}
				if result.HTTPRequests != expected {
					t.Error("wrong request count")
				}
			})
		}
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "cities") {
			writeFixture(t, w, "city")
			return
		}
		writeFixture(t, w, "complex")
	})
	if _, err := c.Show(context.Background(), productRef{ID: testProductID}, "kazan", Analyses(), false); err == nil || !strings.Contains(err.Error(), "instead of requested") {
		t.Fatalf("identity mismatch accepted: %v", err)
	}
}

func TestFailuresStopImmediately(t *testing.T) {
	for _, code := range []int{301, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(code)
			})
			_, err := c.Search(context.Background(), "ттг", "kazan", Analyses(), 1, searchFilters{Deadline: -1}, false)
			if err == nil || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
		})
	}
	for _, body := range []string{`<html>check</html>`, `null`, `{}`, `{"cities":[]}`, `{"cities":[{"id":"bad","slug":"kazan"}]}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		})
		if _, err := c.resolveCity(context.Background(), "kazan"); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestCancellationAndRequestBudget(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.resolveCity(ctx, "kazan"); err == nil {
		t.Error("ignored timeout")
	}
	if c.requests != 1 {
		t.Error("retried timeout")
	}
	c.requests = requestBudget
	if err := c.request(context.Background(), "/golk/test", nil, nil, new(any)); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Error("budget ignored")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	c.requests = 0
	if err := c.request(ctx, "/golk/test", nil, nil, new(any)); err == nil || c.requests != 0 {
		t.Error("sent canceled request")
	}
}

func TestMalformedPaginationAndProducts(t *testing.T) {
	for _, mutation := range []string{"metadata", "count", "id", "duplicate"} {
		t.Run(mutation, func(t *testing.T) {
			var page map[string]any
			if err := json.Unmarshal(fixture(t, "page"), &page); err != nil {
				t.Errorf("decode fixture: %v", err)
				return
			}
			data := page["data"].([]any)
			switch mutation {
			case "metadata":
				page["offset"] = 1
			case "count":
				page["total"] = 10
			case "id":
				data[0].(map[string]any)["id"] = "11111111-1111-1111-1111-111111111111"
			case "duplicate":
				data[1] = data[0]
			}
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "cities") {
					writeFixture(t, w, "city")
					return
				}
				if strings.HasSuffix(r.URL.Path, "analyses") {
					writeFixture(t, w, "search")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(page); err != nil {
					t.Errorf("encode fixture response: %v", err)
				}
			})
			if _, err := c.Search(context.Background(), "ттг", "kazan", Analyses(), 1, searchFilters{Deadline: -1}, false); err == nil {
				t.Error("malformed response accepted")
			}
		})
	}
}

func TestJSONOutputErrors(t *testing.T) {
	var out bytes.Buffer
	if err := writeJSON(&out, make(chan int)); err == nil {
		t.Error("encoding error lost")
	}
}

func TestCheckupDetailsAndCachedUUID(t *testing.T) {
	const id = "2e75e9e8-8bf2-ef11-80f1-00155d3be302"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/golk/addresses/api/v1/cities":
			writeFixture(t, w, "city")
		case "/golk/tests/api/v1/checkups/" + id:
			writeFixture(t, w, "checkup")
		default:
			t.Errorf("unexpected checkup request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	ref, err := parseReference("https://www.invitro.ru/analizes/checkups/kazan/programmy-zdorovya/" + id + "/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Show(context.Background(), ref, ref.City, ref.Kind, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != id || result.CatalogID != id || result.Code != "ЧЕКАП138" || result.Description == "" || result.DeadlineNote != "" || len(result.Tags) == 0 || len(result.IncludedServices) == 0 || len(result.Tests) == 0 || result.Sections["how_to_pass"] == "" || result.HTTPRequests != 2 {
		t.Fatalf("incomplete checkup: %+v", result)
	}
	paths := tempPaths(t)
	if err := paths.remember("kazan", []product{result.product}); err != nil {
		t.Fatal(err)
	}
	entry, err := paths.lookup("kazan", id)
	if err != nil || entry == nil || entry.Type != "checkups" {
		t.Fatalf("checkup cache: %+v %v", entry, err)
	}
}

func TestResponseLimitAndHTMLChallenge(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			if oversize {
				w.Header().Set("Content-Type", "application/json")
				// The client may close the response as soon as it reaches its limit.
				_, _ = w.Write(bytes.Repeat([]byte(" "), maxResponseBytes+1))
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>Verification required</html>")
		})
		_, err := c.resolveCity(context.Background(), "kazan")
		if err == nil || c.requests != 1 {
			t.Fatalf("expected immediate failure, got %v", err)
		}
		if oversize && !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("wrong error %v", err)
		}
		if !oversize && !strings.Contains(err.Error(), "non-JSON") {
			t.Errorf("wrong error %v", err)
		}
	}
}
