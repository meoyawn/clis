package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Synthetic SDUI markup and Flight state preserve the independently captured
// protocol shape. Real names, cookies, tracking IDs and HAR data stay private.
func keywordSearchFixture(t *testing.T, query string) string {
	t.Helper()
	body, err := os.ReadFile("testdata/mutuals.html")
	if err != nil {
		t.Fatal(err)
	}
	q, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	flight := `12:[{"$type":"proto.sdui.actions.core.SetState","value":{"state":{"key":{"key":{"value":{"id":"SearchResultsGlobalTyahKeywordsBinding"}}},"value":{"$case":"stringValue","stringValue":` + string(q) + `}},"isOptimistic":true}}]` + "\n"
	// Flight rows can span script chunks; the decoder must join them first.
	mid := len(flight) / 2
	payload, err := json.Marshal([]string{flight[:mid], flight[mid:]})
	if err != nil {
		t.Fatal(err)
	}
	scripts := "<script>window.__como_rehydration__ = " + string(payload) + "</script>"
	page := strings.Replace(string(body), " • 1st", " • 3rd+", 1)
	return strings.Replace(page, "</body>", `<input data-testid="typeahead-input" componentkey="SearchResults_SearchTyahInputRef" value="">`+scripts+"</body>", 1)
}

// Owns the keyword transport and batch-output contract, which company search
// cannot cover: preserve query bytes, one GET per query, and separate results.
func TestKeywordSearchGroupsQueriesAndPreservesKeywords(t *testing.T) {
	queries := []string{`"Ngô Example" Oulu & (R&D)+100% #?`, `"Example Ngo" Palermo`}
	calls := 0
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls >= len(queries) {
			t.Fatalf("unexpected extra request: %s", r.URL)
		}
		query := queries[calls]
		calls++
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/search/results/people/" || q.Get("keywords") != query || q.Get("origin") != "GLOBAL_SEARCH_HEADER" || len(q) != 2 || r.Header.Get("Accept") != "text/html" {
			t.Fatalf("keyword search request changed: %s %s", r.Method, r.URL)
		}
		return jsonResponse([]byte(keywordSearchFixture(t, query))), nil
	})}}
	result, _, err := client.searchKeywords(context.Background(), queries, 2)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(result.Searches) != 2 || result.FetchedAt == "" {
		t.Fatalf("incomplete batch: calls=%d result=%+v", calls, result)
	}
	for i, search := range result.Searches {
		if search.Query != queries[i] || search.Returned != 2 || !search.HasMore || len(search.People) != 2 {
			t.Fatalf("query results mixed or limit lost: %+v", search)
		}
		if p := search.People[0]; p.Name != "First Mutual" || p.ConnectionDegree != "3rd+" || p.URL != "https://www.linkedin.com/in/first-mutual/" || p.Headline != "Senior Go & Platform Engineer" {
			t.Fatalf("incorrect primary result: %+v", p)
		}
		if p := search.People[1]; p.Name != "Second Mutual" || p.ConnectionDegree != "1st" {
			t.Fatalf("incorrect second result: %+v", p)
		}
	}
}

// A failed second query must not publish the first as a complete batch or
// continue to the third, including HTTP 200 login/changed/empty responses.
func TestKeywordSearchStopsBatchWithoutPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"rate limit", 429, "", "rate limit"},
		{"checkpoint", 403, "", "checkpoint"},
		{"login document", 200, "<html><form>Sign in</form></html>", "checkpoint"},
		{"wrong query", 200, keywordSearchFixture(t, "different query"), "not confirmed"},
		{"unrecognized empty page", 200, `<input type="text" componentkey="SearchResults_SearchTyahInputRef" value="second"><div data-sdui-screen="com.linkedin.sdui.flagshipnav.search.SearchResultsPeople"></div>`, "no recognizable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				switch calls {
				case 1:
					return jsonResponse([]byte(keywordSearchFixture(t, "first"))), nil
				case 2:
					return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
				default:
					t.Fatalf("batch continued after failure: %s", r.URL)
					return nil, fmt.Errorf("unexpected request")
				}
			})}}
			result, _, err := client.searchKeywords(context.Background(), []string{"first", "second", "third"}, 10)
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != 2 || len(result.Searches) != 0 {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}

// Only LinkedIn's explicit visible empty state distinguishes zero matches from
// a changed or restricted page; a hidden alternative must not certify zero.
func TestKeywordSearchRecognizesExplicitEmptyState(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden=%t", hidden), func(t *testing.T) {
			body := fmt.Sprintf(`<input componentkey="SearchResults_SearchTyahInputRef" value="example"><div data-sdui-screen="com.linkedin.sdui.flagshipnav.search.SearchResultsPeople"><section aria-hidden="%t"><h2>No results found</h2><p>Try removing filters or rephrasing your search.</p></section></div>`, hidden)
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse([]byte(body)), nil
			})}}
			result, _, err := client.searchKeywords(context.Background(), []string{"example"}, 10)
			if hidden {
				if err == nil || !strings.Contains(err.Error(), "no recognizable") || len(result.Searches) != 0 {
					t.Fatalf("hidden empty state accepted: %+v, %v", result, err)
				}
				return
			}
			if err != nil || len(result.Searches) != 1 {
				t.Fatalf("explicit empty state rejected: %+v, %v", result, err)
			}
			if s := result.Searches[0]; s.Returned != 0 || s.People == nil || len(s.People) != 0 || s.HasMore {
				t.Fatalf("empty search was misreported: %+v", s)
			}
		})
	}
}
