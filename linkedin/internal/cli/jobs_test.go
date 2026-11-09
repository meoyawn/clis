package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Synthetic cards preserve the captured expanded-JSON contract. The collection
// can contain non-job suggestions and repeat a posting across result pages.
func TestCompanyJobsPaginatesWorldwideAndDeduplicates(t *testing.T) {
	first := fixture(t, "jobs")
	second := strings.Replace(string(first), `"start": 0, "count": 3, "total": 5`, `"start": 3, "count": 2, "total": 5`, 1)
	second = strings.Replace(second, `"urn:li:fsd_jobPosting:201"`, `"urn:li:fsd_jobPosting:203"`, 1)
	second = strings.Replace(second, `    { "jobCardUnion": { "jobSearchSuggestion": { "title": "Try a different search" } } },`+"\n", "", 1)
	calls := 0
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet {
			t.Fatalf("jobs read attempted %s", r.Method)
		}
		if calls == 1 {
			if r.URL.Path != "/voyager/api/graphql" || r.URL.Query().Get("variables") != "(universalName:example-company)" {
				t.Fatalf("wrong company lookup: %s", r.URL)
			}
			return jsonResponse(fixture(t, "company")), nil
		}
		q := r.URL.Query()
		if r.URL.Path != "/voyager/api/voyagerJobsDashJobCards" || q.Get("q") != "jobSearch" || q.Get("count") != "25" || r.Header.Get("Accept") != "application/json" {
			t.Fatalf("wrong jobs request: %s %v", r.URL, r.Header)
		}
		if q.Get("query") != "(origin:COMPANY_PAGE_JOBS_CLUSTER_EXPANSION,locationUnion:(geoId:92000000),selectedFilters:(company:List(123456789)),spellCorrectionEnabled:true)" || strings.Contains(r.URL.RawQuery, "%28") {
			t.Fatalf("company/worldwide filter or Rest.li encoding changed: %s", r.URL)
		}
		switch calls {
		case 2:
			if q.Get("start") != "0" {
				t.Fatalf("first offset = %q", q.Get("start"))
			}
			return jsonResponse(first), nil
		case 3:
			if q.Get("start") != "3" {
				t.Fatalf("continuation ignored non-job result: %q", q.Get("start"))
			}
			return jsonResponse([]byte(second)), nil
		default:
			t.Fatalf("unexpected request %d", calls)
			return nil, fmt.Errorf("unexpected request")
		}
	})}}
	company, err := parseCompany("https://www.linkedin.com/company/example-company/jobs/")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.jobs(context.Background(), company, jobsOptions{limit: 1, all: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || result.Company.ID != "123456789" || result.Total != 5 || result.Returned != 3 || len(result.Jobs) != 3 || result.HasMore || result.NextStart != nil || result.FetchedAt == "" {
		t.Fatalf("incomplete result: calls=%d result=%+v", calls, result)
	}
	for i, id := range []string{"201", "202", "203"} {
		if result.Jobs[i].ID != id || result.Jobs[i].URL != "https://www.linkedin.com/jobs/view/"+id+"/" {
			t.Fatalf("wrong order, duplicate, or job URL: %+v", result.Jobs)
		}
	}
	job := result.Jobs[0]
	if job.Title != "Senior Platform Engineer" || job.Company != "Example Company" || job.Location != "London, England (Hybrid)" || job.Details != "$150K–$200K/yr" || job.ListedAt != "2026-11-07T12:00:00Z" || result.Jobs[1].ListedAt != "" {
		t.Fatalf("card fields or date units changed: %+v", result.Jobs)
	}
}

// A resumed, bounded request must neither re-read offset zero nor skip results
// when handing its next offset back to the caller. Numeric IDs need no lookup.
func TestCompanyJobsLimitAndResume(t *testing.T) {
	var page map[string]any
	if err := json.Unmarshal(fixture(t, "jobs"), &page); err != nil {
		t.Fatal(err)
	}
	page["paging"] = map[string]int{"start": 2, "count": 1, "total": 5}
	page["elements"] = page["elements"].([]any)[2:]
	body, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/voyager/api/voyagerJobsDashJobCards" || r.URL.Query().Get("start") != "2" || r.URL.Query().Get("count") != "1" {
			t.Fatalf("wrong resumed request: %s", r.URL)
		}
		return jsonResponse(body), nil
	})}}
	result, err := client.jobs(context.Background(), companyRef{ID: "123456789"}, jobsOptions{start: 2, limit: 1})
	if err != nil || calls != 1 || result.Start != 2 || result.Returned != 1 || !result.HasMore || result.NextStart == nil || *result.NextStart != 3 {
		t.Fatalf("limit/resume: calls=%d result=%+v err=%v", calls, result, err)
	}
}

// After a successful page, failures must discard the partial result and stop.
// These protocol failures are not covered by people-search pagination tests.
func TestCompanyJobsRejectsIncompleteReads(t *testing.T) {
	first := string(fixture(t, "jobs"))
	second := strings.Replace(first, `"start": 0`, `"start": 3`, 1)
	second = strings.Replace(second, `"total": 5`, `"total": 6`, 1)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"rate limit", 429, "", "rate limit"},
		{"checkpoint", 403, "", "checkpoint"},
		{"login HTML", 200, "<html>Sign in</html>", "valid jobs JSON"},
		{"unknown response", 200, `{}`, "missing paging or elements"},
		{"missing total", 200, strings.Replace(second, `, "total": 6`, "", 1), "missing paging"},
		{"wrong offset", 200, first, "unexpected jobs paging"},
		{"country substitution", 200, strings.Replace(second, "urn:li:fsd_geo:92000000", "urn:li:fsd_geo:103644278", 1), "Worldwide scope"},
		{"wrong company", 200, strings.Replace(second, "urn:li:fsd_company:123456789", "urn:li:fsd_company:999", 1), "does not confirm company"},
		{"bad job ID", 200, strings.Replace(second, "urn:li:fsd_jobPosting:201", "urn:li:fsd_jobPosting:bad", 1), "invalid job URN"},
		{"incomplete card", 200, strings.Replace(second, `"jobPostingTitle": "Senior Platform Engineer"`, `"jobPostingTitle": ""`, 1), "missing its title"},
		{"unknown union", 200, strings.Replace(second, `"jobSearchSuggestion"`, `"newResultType"`, 1), "unrecognized result"},
		{"repeated page", 200, second, "no new jobs"},
		{"disappearing results", 200, `{"metadata":{"geo":{"entityUrn":"urn:li:fsd_geo:92000000"}},"paging":{"start":3,"count":25,"total":0},"elements":[]}`, "stopped returning jobs"},
		{"short page", 200, strings.Replace(strings.Replace(second, `"count": 3`, `"count": 25`, 1), `"total": 6`, `"total": 7`, 1), "incomplete jobs paging"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return jsonResponse([]byte(first)), nil
				}
				if calls > 2 {
					t.Fatal("retried or continued after failure")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}}
			result, err := client.jobs(context.Background(), companyRef{ID: "123456789"}, jobsOptions{all: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != 2 || len(result.Jobs) != 0 {
				t.Fatalf("calls=%d result=%+v err=%v; want %q", calls, result, err, tc.want)
			}
		})
	}
}

func TestCompanyJobsRequiresExplicitEmptyCollection(t *testing.T) {
	for _, elements := range []string{"[]", "null"} {
		t.Run(elements, func(t *testing.T) {
			body := `{"metadata":{"geo":{"entityUrn":"urn:li:fsd_geo:92000000"}},"paging":{"start":0,"count":25,"total":0},"elements":` + elements + `}`
			client := linkedinClient{http: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return jsonResponse([]byte(body)), nil
			})}}
			result, err := client.jobs(context.Background(), companyRef{ID: "123456789"}, jobsOptions{all: true})
			if elements == "null" {
				if err == nil || !strings.Contains(err.Error(), "missing paging or elements") {
					t.Fatalf("missing collection mistaken for zero jobs: %+v %v", result, err)
				}
				return
			}
			if err != nil || result.Jobs == nil || result.Total != 0 || result.Returned != 0 || result.HasMore || result.NextStart != nil {
				t.Fatalf("explicit empty collection rejected: %+v %v", result, err)
			}
		})
	}
}
