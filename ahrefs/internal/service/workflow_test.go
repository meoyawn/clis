package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/meoyawn/clis/ahrefs/internal/domain"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Route the real HTTP client to a local server without adding a runtime base-URL
// override to authenticated commands.
func routeTo(t *testing.T, client *http.Client, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport.(*http.Transport).Clone()
	}
	client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "app.ahrefs.com" {
			t.Errorf("unexpected outbound host %q", r.URL.Host)
			return nil, fmt.Errorf("unexpected test destination")
		}
		clone := r.Clone(r.Context())
		clone.URL.Scheme, clone.URL.Host = origin.Scheme, origin.Host
		return next.RoundTrip(clone)
	})
	t.Cleanup(client.CloseIdleConnections)
}

type workflowFixture struct {
	Projects json.RawMessage   `json:"projects"`
	Crawls   json.RawMessage   `json:"crawls"`
	Issues   json.RawMessage   `json:"issues"`
	Diffs    json.RawMessage   `json:"diffs"`
	Pages    []json.RawMessage `json:"pages"`
}

func loadWorkflow(t *testing.T) workflowFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture workflowFixture
	if err := json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func issueResponse(t *testing.T, fixture workflowFixture) []byte {
	t.Helper()
	var entries []struct {
		Issue json.RawMessage `json:"issue"`
	}
	if err := json.Unmarshal(fixture.Issues, &entries); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{"issue": entries[0].Issue})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertRequest(t *testing.T, body map[string]json.RawMessage, key, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(body[key], &gotValue); err != nil {
		t.Errorf("request %s: %v", key, err)
		return
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("request %s = %s; want %s", key, body[key], want)
	}
}

func TestAuditWorkflow(t *testing.T) {
	fixture := loadWorkflow(t)
	detail := issueResponse(t, fixture)
	httpClient := &http.Client{}
	tableRequests := 0
	routeTo(t, httpClient, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Origin") != Origin {
			t.Errorf("unexpected request method/origin: %s %q", r.Method, r.Header.Get("Origin"))
		}
		w.Header().Set("Content-Type", "application/json")
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		endpoint := strings.TrimPrefix(r.URL.Path, "/v4/")
		if endpoint != "saProjects" {
			assertRequest(t, body, "project_id", `"123"`)
		}
		if endpoint == "saGetProjectIssues" || endpoint == "saGetDiffsByIssues" || endpoint == "saDeTable" {
			assertRequest(t, body, "timestamp", `{"timestamp":["Some","2026-01-21T12:00:00Z"],"compare_with":["Some","2026-01-14T12:00:00Z"]}`)
			assertRequest(t, body, "global_filter_id", `null`)
		}
		var response []byte
		switch endpoint {
		case "saProjects":
			response = fixture.Projects
		case "saCrawls":
			response = fixture.Crawls
		case "saGetProjectIssues":
			response = fixture.Issues
		case "saGetDiffsByIssues":
			assertRequest(t, body, "timestamp2", `{"timestamp":["Some","2026-01-14T12:00:00Z"],"compare_with":["Some","2026-01-07T12:00:00Z"]}`)
			response = fixture.Diffs
		case "saGetProjectIssueById":
			assertRequest(t, body, "issue_id", `"synthetic-og"`)
			response = detail
		case "saDeTable":
			assertRequest(t, body, "size", `1`)
			assertRequest(t, body, "from", fmt.Sprint(tableRequests))
			assertRequest(t, body, "with_prev_value", `false`)
			assertRequest(t, body, "de_mode", `null`)
			assertRequest(t, body, "input", `{"type":"Pages","fields":[["Simple","pageRating"],["Simple","url"],["Simple","traffic"],["Simple","ogTagsValid"],["Simple","ogTagsAttributes"],["Simple","ogTagsValue"],["Simple","depth"],["Simple","compliant"],["Simple","incomingAllLinks"]],"filter":["FilterId","synthetic-filter"],"sort":[{"by":["Simple","pageRating"],"order":"desc"}]}`)
			if tableRequests >= len(fixture.Pages) {
				t.Error("unexpected extra page request")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response = fixture.Pages[tableRequests]
			tableRequests++
		default:
			t.Errorf("unexpected endpoint %s", endpoint)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(response)
	})
	client := NewClient(httpClient)
	ctx := t.Context()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != "123" || projects[0].LastCrawl.ID != 3 {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
	crawls, err := client.Crawls(ctx, "123")
	if err != nil || len(crawls) != 4 || crawls[0].Status != "Running" {
		t.Fatalf("crawls = %+v, %v", crawls, err)
	}
	q := domain.Query{ProjectID: "123"}
	issues, err := client.Issues(ctx, q, IssueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues.Issues) != 2 || issues.Issues[0].ID != "synthetic-og" || *issues.Issues[0].Count != 2 {
		t.Fatalf("actual issues = %+v", issues)
	}
	changes := issues.Issues[0].Changes
	if changes == nil || *changes.Delta != 1 || *changes.New != 2 || *changes.Added != 0 {
		t.Fatalf("changes = %+v", changes)
	}
	if issues.Issues[1].Changes.New != nil || *issues.Issues[1].Changes.Delta != 0 {
		t.Fatalf("null/zero changes were lost: %+v", issues.Issues[1].Changes)
	}
	issue, err := client.Issue(ctx, "123", "synthetic-og")
	if err != nil || issue.FilterID != "synthetic-filter" || len(issue.Columns) != 9 || issue.Description != "Some required OG tags are missing." || issue.HowToFix != "Add the missing tags to each affected page." {
		t.Fatalf("issue details = %+v, %v", issue, err)
	}
	q.IssueID = issue.ID
	pages, err := client.Pages(ctx, q, PageOptions{Limit: 1, All: true, SortBy: "pageRating", Direction: domain.Descending()})
	if err != nil {
		t.Fatal(err)
	}
	if pages.Total != 2 || len(pages.Rows) != 2 || pages.NextOffset != nil || tableRequests != 2 {
		t.Fatalf("pagination = %+v; requests = %d", pages, tableRequests)
	}
	first, second := pages.Rows[0], pages.Rows[1]
	if first["id"] != "18446744073709551615" || first["incomingAllLinks"] != json.Number("9007199254740993") || first["traffic"] != json.Number("0") || first["ogTagsValid"] != false || first["depth"] != json.Number("0") {
		t.Fatalf("scalar values were changed: %+v", first)
	}
	attributes := first["ogTagsAttributes"].([]any)
	values := first["ogTagsValue"].([]any)
	if len(attributes) != 6 || attributes[5] != "og:locale" || values[5] != "tt_RU" {
		t.Fatalf("Open Graph lists were truncated: %v %v", attributes, values)
	}
	if second["pageRating"] != nil || second["traffic"] != nil || second["compliant"] != false || len(second["ogTagsValue"].([]any)) != 0 {
		t.Fatalf("null/empty/false values were changed: %+v", second)
	}
}

func TestPageOverridesAndNextOffset(t *testing.T) {
	fixture := loadWorkflow(t)
	detail := issueResponse(t, fixture)
	httpClient := &http.Client{}
	tableRequests := 0
	routeTo(t, httpClient, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/v4/saCrawls":
			_, _ = w.Write(fixture.Crawls)
		case "/v4/saGetProjectIssueById":
			_, _ = w.Write(detail)
		case "/v4/saDeTable":
			tableRequests++
			assertRequest(t, body, "timestamp", `{"timestamp":["Some","2026-01-14T12:00:00Z"],"compare_with":"None"}`)
			assertRequest(t, body, "from", `1`)
			assertRequest(t, body, "input", `{"type":"Pages","fields":[["Simple","url"],["Simple","httpCode"]],"filter":["FilterId","custom-filter"],"sort":[{"by":["Simple","url"],"order":"asc"}]}`)
			_, _ = io.WriteString(w, `["Ok",{"total":3,"table":{"headers":[["Simple","url"],["Simple","httpCode"]],"rows":[{"id":"99","values":[["Url","https://example.test/second/"],["Long",200]]}]}}]`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	none := ""
	pages, err := NewClient(httpClient).Pages(t.Context(), domain.Query{
		ProjectID: "123", Crawl: "14-01-2026T120000", CompareWith: &none,
		IssueID: "synthetic-og", FilterID: "custom-filter", Columns: []string{"url", "httpCode"},
	}, PageOptions{Limit: 1, Offset: 1, SortBy: "url", Direction: domain.Ascending()})
	if err != nil {
		t.Fatal(err)
	}
	if pages.Offset != 1 || pages.NextOffset == nil || *pages.NextOffset != 2 || len(pages.Rows) != 1 || tableRequests != 1 || pages.CompareWith != "" {
		t.Fatalf("single page = %+v; requests = %d", pages, tableRequests)
	}
}

func TestIncompletePaginationReturnsNoPartialResult(t *testing.T) {
	for _, test := range []struct {
		name, response, message string
	}{
		{"empty page", `["Ok",{"total":2,"table":{"headers":[["Simple","url"]],"rows":[]}}]`, "empty page"},
		{"unknown value", `["Ok",{"total":2,"table":{"headers":[["Simple","url"]],"rows":[{"id":"2","values":[["NewTag","secret"]]}]}}]`, "unsupported table cell tag"},
		{"missing value", `["Ok",{"total":2,"table":{"headers":[["Simple","url"]],"rows":[{"id":"2","values":[]}]}}]`, "values for"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := loadWorkflow(t)
			httpClient := &http.Client{}
			pageRequests := 0
			routeTo(t, httpClient, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v4/saCrawls" {
					_, _ = w.Write(fixture.Crawls)
					return
				}
				if r.URL.Path != "/v4/saDeTable" {
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				pageRequests++
				if pageRequests == 1 {
					_, _ = io.WriteString(w, `["Ok",{"total":2,"table":{"headers":[["Simple","url"]],"rows":[{"id":"1","values":[["Url","https://example.test/"]]}]}}]`)
				} else {
					_, _ = io.WriteString(w, test.response)
				}
			})
			result, err := NewClient(httpClient).Pages(t.Context(), domain.Query{ProjectID: "123", FilterID: "synthetic-filter", Columns: []string{"url"}}, PageOptions{Limit: 1, All: true, SortBy: "url", Direction: domain.Ascending()})
			if err == nil || !strings.Contains(err.Error(), test.message) || result != nil || pageRequests != 2 {
				t.Fatalf("result = %+v, err = %v, requests = %d", result, err, pageRequests)
			}
		})
	}
}

func TestRejectedResponsesStopWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, "SYNTHETIC_SECRET"},
		{"forbidden", 403, "SYNTHETIC_SECRET"},
		{"rate limited", 429, "SYNTHETIC_SECRET"},
		{"login page", 200, "<html>SYNTHETIC_SECRET</html>"},
		{"API error", 200, `["Error",["NotAuthenticated","SYNTHETIC_SECRET"]]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{}
			requests := 0
			routeTo(t, httpClient, func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			result, err := NewClient(httpClient).Projects(context.Background())
			if err == nil || result != nil || requests != 1 || strings.Contains(err.Error(), "SYNTHETIC_SECRET") {
				t.Fatalf("result = %+v, err = %v, requests = %d", result, err, requests)
			}
			if test.status == 429 && !strings.Contains(err.Error(), "Retry-After: 60") {
				t.Fatalf("missing retry guidance: %v", err)
			}
		})
	}
}
