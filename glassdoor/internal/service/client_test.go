package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/glassdoor/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeSalaryReport(t *testing.T) {
	for _, tc := range []struct {
		name, period, currency, kind, reportType, key string
		count                                         int
	}{
		{"country monthly", "MONTHLY", "RUB", "N", "COUNTRY", "countryId", 7},
		{"city annual", "ANNUAL", "EUR", "C", "CITY", "cityId", 25},
		{"remote provider estimate", "ANNUAL", "USD", "S", "STATE", "stateId", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			points := func(a, b, c int) []any {
				return []any{map[string]any{"percentile": "P_75TH", "value": c}, map[string]any{"percentile": "P_25TH", "value": a}, map[string]any{"percentile": "P_50TH", "value": b}}
			}
			client := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/bff/employer-profile-mono/occ-salary-estimates" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if len(input) != 4 || input[tc.key] != float64(205) || input["yearsOfExperience"] != "SEVEN_TO_NINE" || input["jobTitle"] != "Golang Developer" {
					t.Fatalf("wrong native scope: %+v", input)
				}
				body := map[string]any{"data": map[string]any{"occSalaryEstimates": map[string]any{
					"basePayPercentiles": points(180000, 210000, 300000), "additionalPayPercentiles": points(10000, 20000, 30000), "totalPayPercentiles": points(190000, 230000, 330000),
					"confidence": "HIGH", "currency": map[string]any{"code": tc.currency}, "estimateSourceUpdateTime": "2022-11-01T01:49:55", "jobTitle": map[string]any{"id": 123, "text": "Golang Developer"}, "payPeriod": tc.period, "queryLocation": map[string]any{"id": 205, "type": tc.reportType}, "salariesCount": tc.count,
				}}}
				b, _ := json.Marshal(body)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})}, nil)
			r, err := client.Estimate(context.Background(), domain.Query{Title: domain.Title{ID: 123, JobTitle: "Golang Developer"}, Location: domain.Location{Type: tc.kind, LocationID: 205, Name: "Test Market"}, Experience: domain.SevenToNine()})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !r.Available || r.BasePay.P50.Amount != 210000 || r.TotalPay.P50.Amount != 230000 || r.SalaryCount != tc.count {
				t.Fatalf("wrong report: %+v", r)
			}
			data, _ := json.Marshal(r)
			var out map[string]any
			if err = json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			money := out["base_pay"].(map[string]any)["p50"].(map[string]any)
			interval := "year"
			if tc.period == "MONTHLY" {
				interval = "month"
			}
			if money["amount"] != float64(210000) || money["currency"] != tc.currency || money["interval"] != interval {
				t.Fatalf("money lost interval: %+v", money)
			}
			if r.SourceUpdatedAt != "2022-11-01T01:49:55" {
				t.Fatal("source observation date lost")
			}
		})
	}
}

func TestCompanySalaryFiltersAndNativeUnits(t *testing.T) {
	for _, location := range []string{"Synthetic Market", "Unexpected US default"} {
		t.Run(location, func(t *testing.T) {
			calls := 0
			c := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if r.URL.Path != "/bff/employer-profile-mono/agg-salary-estimates" || r.Method != "POST" || input["employerId"] != float64(456) || input["countryId"] != float64(6) || input["payPeriod"] != "ANNUAL" || input["jobTitle"] != "Engineer" || input["page"] != float64(2) {
					t.Fatalf("wrong company/location/title/period/page: %+v", input)
				}
				body := `{"data":{"aggregatedSalaryEstimates":{"jobTitleCount":12,"numPages":2,"queryLocation":{"name":"` + location + `"},"results":[{"basePayStatistics":{"percentiles":[{"ident":"P25","value":360000},{"ident":"P50","value":420000},{"ident":"P75","value":480000}]},"totalPayStatistics":{"percentiles":[{"ident":"P25","value":400000},{"ident":"P50","value":460000},{"ident":"P75","value":520000}]},"currency":{"code":"AED"},"jobTitle":{"id":123,"text":"Software Engineer"},"mostRecent":"2026-05-25T05:00:31","salaryCount":1}]}}}`
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, nil)
			r, err := c.CompanySalaries(context.Background(), domain.CompanyQuery{Employer: domain.Employer{ID: 456, Name: "Synthetic Company"}, Location: domain.Location{LocationID: 6, Type: "N", Name: "Synthetic Market"}, Title: "Engineer", Period: domain.Year(), Page: 2})
			if location != "Synthetic Market" {
				if err == nil || calls != 1 {
					t.Fatalf("wrong-location response accepted/retried: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(r.Results) != 1 || r.NumPages != 2 || r.JobTitleCount != 12 || r.Results[0].SalaryCount != 1 {
				t.Fatalf("company coverage lost: %+v", r)
			}
			row := r.Results[0]
			if row.BasePay.P50.Amount != 420000 || row.TotalPay.P50.Amount != 460000 || row.BasePay.P50.Currency != "AED" || row.BasePay.P50.Interval != domain.Year() || row.MostRecent != "2026-05-25T05:00:31" {
				t.Fatalf("native component/units/date changed: %+v", row)
			}
		})
	}
}

func TestNoReportOnAccessOrChangedScope(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, mitigated string
		status                             int
		human                              bool
	}{
		{name: "rate limit", status: 429, body: `<title>Just a moment...</title>PRIVATE_ERROR_BODY`, contentType: "text/html"},
		{name: "ordinary forbidden", status: 403, body: `PRIVATE_ERROR_BODY`},
		{name: "challenge header", status: 403, body: `PRIVATE_ERROR_BODY`, mitigated: "challenge", human: true},
		{name: "challenge HTML", status: 403, body: `<title>Just a moment...</title>PRIVATE_ERROR_BODY`, contentType: "text/html", human: true},
		{name: "challenge with 200", status: 200, body: `<h1>Humans only</h1>PRIVATE_ERROR_BODY`, contentType: "text/html", human: true},
		{name: "server errors", status: 200, body: `{"errors":[{"message":"PRIVATE_ERROR_BODY"}]}`},
		{name: "missing data", status: 200, body: `{"data":{"occSalaryEstimates":null}}`},
		{name: "wrong location", status: 200, body: `{"data":{"occSalaryEstimates":{"jobTitle":{"id":123,"text":"Golang Developer"},"queryLocation":{"id":999,"type":"COUNTRY"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				contentType := tc.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {contentType}, "Cf-Mitigated": {tc.mitigated}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}, nil)
			_, err := c.Estimate(context.Background(), domain.Query{Title: domain.Title{ID: 123, JobTitle: "Golang Developer"}, Location: domain.Location{Type: "N", LocationID: 205, Name: "Russia"}, Experience: domain.AllExperience()})
			if err == nil || calls != 1 || strings.Contains(err.Error(), "PRIVATE_ERROR_BODY") {
				t.Fatalf("unexpected retry/leak/report: calls=%d err=%v", calls, err)
			}
			var challenge *HumanVerificationError
			if errors.As(err, &challenge) != tc.human {
				t.Fatalf("human-verification classification: expected %v, got %v", tc.human, err)
			}
		})
	}
}

func TestIndustryCatalogFromFragmentedFlightJSON(t *testing.T) {
	// One JSON record split over two script chunks, with unrelated executable
	// JavaScript. The parser reads JSON only and rejects a changed/blocked page.
	record := `a:["$","Component",null,{"industries":[{"id":17,"name":"Synthetic industry"}]}]` + "\n"
	first, _ := json.Marshal(record[:32])
	second, _ := json.Marshal(record[32:])
	page := `<script>self.__next_f.push([1,` + string(first) + `]);</script><script>throw new Error("must not execute");self.__next_f.push([1,` + string(second) + `]);</script>`
	values, err := parseIndustries(strings.NewReader(page))
	if err != nil || len(values) != 1 || values[0].ID != 17 {
		t.Fatalf("failed catalog extraction: %v %v", values, err)
	}
	if _, err = parseIndustries(strings.NewReader(`<html>Sign in or complete a challenge</html>`)); err == nil {
		t.Fatal("challenge cached as empty catalog")
	}
}
