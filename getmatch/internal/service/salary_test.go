package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Synthetic protocol responses deliberately include private profile fields.
// The report must normalize aggregate thousands without leaking those fields.
func TestEstimateAggregateContract(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		median  int
		wantErr bool
	}{
		{"matching sample", 3, 350000, false},
		{"disagreeing samples", 4, 350000, false},
		{"separately scoped medians", 3, 360000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.Query().Encode() != "l=other_russia&se=senior&sp=golang&wf=remote" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				var body any
				switch r.URL.Path {
				case "/api/salaries":
					body = map[string]any{"percentile_25": 300, "percentile_50": 350, "percentile_90": 470, "salary_display": map[string]any{"salary": "PRIVATE_PROFILE_SENTINEL"}, "histogram": map[string]any{"x": []int{100, 200}, "y": []int{1, 2}}}
				case "/api/salaries/stats":
					body = map[string]any{"salary_summary": map[string]any{"category_count": tc.count, "category_median": tc.median, "category_avg_bonus": "1.2", "category_has_bonus_share": 50, "salary": "PRIVATE_PROFILE_SENTINEL"}, "salary_display": "PRIVATE_PROFILE_SENTINEL", "most_paying_companies": []any{map[string]any{"name_ru_company": "Surf Telecom", "min_salary": 100000, "median_salary": 200000, "max_salary": 300000}}}
				default:
					t.Fatalf("unexpected endpoint: %s", r.URL.Path)
				}
				b, _ := json.Marshal(body)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})}, nil)
			q := domain.Query{Specialization: domain.Specialization{Slug: "golang"}, Level: domain.Senior(), City: domain.OtherRussia(), WorkFormat: domain.Remote()}
			report, err := client.Estimate(context.Background(), q, "Surf", 160)
			if tc.wantErr {
				if err == nil {
					t.Fatal("mixed medians accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || !report.Available || report.Percentiles.P50 != 350000 || report.Percentiles.P90 != 470000 {
				t.Fatalf("wrong normalization/report: %+v", report)
			}
			if report.SourceCategoryCount == nil || *report.SourceCategoryCount != tc.count || report.HistogramCount != 3 || report.SampleSize == nil || *report.SampleSize != 3 {
				t.Fatalf("sample discrepancy hidden: %+v", report)
			}
			if report.Bonus.AverageMonthlySalaries == nil || *report.Bonus.AverageMonthlySalaries != 1.2 || report.HourlyEquivalent.Percentiles.P50 != 2187.5 {
				t.Fatalf("bonus/hourly units changed: %+v", report)
			}
			if len(report.Company.Matches) != 0 || report.Company.EmployerConfirmedBudget != nil {
				t.Fatal("unrelated employer became confirmed budget")
			}
			b, _ := json.Marshal(report)
			if strings.Contains(string(b), "PRIVATE_PROFILE_SENTINEL") {
				t.Fatal("private salary leaked")
			}
		})
	}
}

func TestAccessFailureStopsWithoutSecondRequest(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("PRIVATE_RESPONSE_BODY"))}, nil
			})}, nil)
			_, err := c.Estimate(context.Background(), domain.Query{Specialization: domain.Specialization{Slug: "golang"}, Level: domain.Senior(), City: domain.AllCities(), WorkFormat: domain.Remote()}, "", 0)
			if err == nil || calls != 1 || strings.Contains(err.Error(), "PRIVATE_RESPONSE_BODY") {
				t.Fatalf("access failure retried/leaked: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestMissingChartIsNotAnEmptyCohort(t *testing.T) {
	c := NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b := "{}"
		if strings.HasSuffix(r.URL.Path, "/stats") {
			b = `{"salary_summary":{"category_count":0}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(b))}, nil
	})}, nil)
	_, err := c.Estimate(context.Background(), domain.Query{Specialization: domain.Specialization{Slug: "golang"}, Level: domain.Senior(), City: domain.AllCities(), WorkFormat: domain.Remote()}, "", 0)
	if err == nil {
		t.Fatal("missing primary chart became a zero-sized salary report")
	}
}
