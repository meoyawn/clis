package service

import (
	"context"
	"encoding/json"
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
			if calls != 1 || !r.Available || r.BasePay.P50.Amount != 210000 || r.TotalPay.P50.Amount != 230000 || r.SalaryCount != tc.count || r.Basis != nil {
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
			if r.SourceUpdatedAt != "2022-11-01T01:49:55" || len(r.Limitations) < 4 {
				t.Fatal("source age/sample caveats lost")
			}
		})
	}
}

func TestNoReportOnAccessOrChangedScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"rate limit", 429, `PRIVATE_ERROR_BODY`},
		{"challenge", 403, `PRIVATE_ERROR_BODY`},
		{"server errors", 200, `{"errors":[{"message":"PRIVATE_ERROR_BODY"}]}`},
		{"missing data", 200, `{"data":{"occSalaryEstimates":null}}`},
		{"wrong location", 200, `{"data":{"occSalaryEstimates":{"jobTitle":{"id":123,"text":"Golang Developer"},"queryLocation":{"id":999,"type":"COUNTRY"}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}, nil)
			_, err := c.Estimate(context.Background(), domain.Query{Title: domain.Title{ID: 123, JobTitle: "Golang Developer"}, Location: domain.Location{Type: "N", LocationID: 205, Name: "Russia"}, Experience: domain.AllExperience()})
			if err == nil || calls != 1 || strings.Contains(err.Error(), "PRIVATE_ERROR_BODY") {
				t.Fatalf("unexpected retry/leak/report: calls=%d err=%v", calls, err)
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
