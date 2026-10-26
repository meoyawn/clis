package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/levels-fyi/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLocationsAcceptsNativeJSONArray(t *testing.T) {
	c, err := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/geo/autocompleteCity" || r.URL.Query().Get("q") != "Example" {
			t.Fatalf("wrong autocomplete request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`[{"id":"123","countryId":"456","name":"Example City","coordinates":"DO_NOT_EXPORT"}]`))}, nil
	})}, http.Header{"Authorization": {"Bearer synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	locations, err := c.Locations(context.Background(), "Example")
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 1 || locations[0].ID != "123" || locations[0].CountryID != "456" || locations[0].Name != "Example City" {
		t.Fatalf("native city identifiers were lost: %+v", locations)
	}
	encoded, err := json.Marshal(locations)
	if err != nil || strings.Contains(string(encoded), "DO_NOT_EXPORT") {
		t.Fatal("unrequested location metadata leaked")
	}
}

func TestGlobalRemoteNativeReport(t *testing.T) {
	fixture, err := os.ReadFile("testdata/search.encrypted.json")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c, err := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		values := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/v3/salary/search" || values.Get("workArrangement") != "remote" || values.Get("minYearsOfExp") != "7" || values.Get("searchText") != "golang" || values.Get("currency") != "USD" || values.Get("sinceDate") != "2026-01-01T00:00:00Z" {
			t.Fatalf("wrong native query: %s %s", r.Method, r.URL)
		}
		for key := range values {
			if strings.Contains(strings.ToLower(key), "country") {
				t.Fatalf("global query restricted to a country: %s", r.URL)
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(fixture)))}, nil
	})}, http.Header{"Authorization": {"Bearer synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Estimate(context.Background(), domain.Query{Role: "golang", JobFamily: "software-engineer", Level: domain.Senior(), Remote: true, Limit: 50, MaxPages: 5}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || r.RetrievedCount != 3 || len(r.CountryCoverage) != 2 || len(r.Groups) != 2 || len(r.Countries) != 3 {
		t.Fatalf("wrong coverage or mixed tax groups: %+v", r)
	}
	if r.Submissions[1].Base.Currency != "USD" || r.Submissions[1].Base.Interval != "year" || r.Submissions[1].Base.Amount != 40000 || r.Submissions[1].OriginalBaseCurrency != "INR" {
		t.Fatal("original-currency label corrupted normalized USD amount")
	}
	gross := r.Groups[0]
	if gross.Basis != "gross" || gross.Base.P50.Amount != 70000 || gross.Total.P50.Amount != 82500 || gross.Stock.Count != 1 || gross.Bonus.Count != 1 {
		t.Fatalf("wrong distributions or absent components treated as zero: %+v", gross)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "DO_NOT_EXPORT") {
		t.Fatal("unallowlisted account data leaked")
	}
}
func TestAccessAndScopeChangesStop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `PRIVATE_RESPONSE`}, {"rate limit", 429, `PRIVATE_RESPONSE`},
		{"hidden", 200, `{"total":10,"hidden":7,"rows":[]}`},
		{"no metadata", 200, `{"rows":[]}`},
		{"wrong arrangement", 200, `{"total":1,"hidden":0,"rows":[{"uuid":"synthetic","jobFamilySlug":"software-engineer","yearsOfExperience":10,"workArrangement":"office"}]}`},
		{"invalid cipher", 200, `{"payload":"invalid"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, err := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}, http.Header{"Authorization": {"Bearer synthetic"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Estimate(context.Background(), domain.Query{Role: "golang", JobFamily: "software-engineer", Level: domain.Senior(), Remote: true, Limit: 50, MaxPages: 5}, time.Time{})
			if err == nil || calls != 1 || strings.Contains(err.Error(), "PRIVATE_RESPONSE") {
				t.Fatalf("failure retried/leaked/accepted: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPerFieldAnonymityDoesNotInventAnEmployer(t *testing.T) {
	body := `{"total":1,"hidden":0,"rows":[{"uuid":"synthetic-anonymous","company":false,"companySlug":false,"location":"Synthetic city","countryId":113,"jobFamilySlug":"software-engineer","level":false,"focusTag":"Golang","yearsOfExperience":"5-10","workArrangement":"remote","salaryFormat":"gross","employmentType":"full_time","baseSalary":50000,"totalCompensation":50000,"avgAnnualStockGrantValue":false,"hidden":false,"isRedacted":false,"anonymise":false}]}`
	c, err := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, http.Header{"Authorization": {"Bearer synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Estimate(context.Background(), domain.Query{Role: "golang", JobFamily: "software-engineer", Level: domain.Senior(), Remote: true, Limit: 50, MaxPages: 1}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	row := r.Submissions[0]
	if row.Company != nil || row.Level != nil || row.Stock != nil || row.Years != nil || row.YearsRange != "5-10" || len(row.AnonymizedFields) != 4 || row.Base.Amount != 50000 {
		t.Fatalf("anonymity markers lost: %+v", row)
	}
	if r.Groups[0].Base.Count != 1 || r.Groups[0].Stock != nil {
		t.Fatal("anonymous metadata prevented valid salary evidence or invented stock")
	}
}
