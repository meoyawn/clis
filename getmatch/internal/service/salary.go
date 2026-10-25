package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

// Allowlisted aggregate fields deliberately exclude salary_display and the
// signed-in user's salary nested under salary_summary.
type salaryData struct {
	P25       *float64 `json:"percentile_25"`
	P50       *float64 `json:"percentile_50"`
	P90       *float64 `json:"percentile_90"`
	Histogram struct {
		X []float64 `json:"x"`
		Y []int     `json:"y"`
	} `json:"histogram"`
}

type statsData struct {
	Summary *struct {
		Count       *int     `json:"category_count"`
		Median      *float64 `json:"category_median"`
		BonusMonths *string  `json:"category_avg_bonus"`
		BonusShare  *float64 `json:"category_has_bonus_share"`
	} `json:"salary_summary"`
	Companies []domain.CompanySalary `json:"most_paying_companies"`
}

func (c *Client) Estimate(ctx context.Context, q domain.Query, company string, hours float64) (*domain.Report, error) {
	if _, err := domain.NewLevelFromValue(q.Level.String()); err != nil {
		return nil, err
	}
	if _, err := domain.NewCityFromValue(q.City.String()); err != nil {
		return nil, err
	}
	if _, err := domain.NewWorkFormatFromValue(q.WorkFormat.String()); err != nil {
		return nil, err
	}
	if q.Specialization.Slug == "" {
		return nil, fmt.Errorf("specialization is empty; select a value from salaries filters")
	}
	if math.IsNaN(hours) || math.IsInf(hours, 0) || hours < 0 {
		return nil, fmt.Errorf("invalid hours-per-month %v: expected a positive number or omit the option", hours)
	}
	values := q.Values()
	var salary salaryData
	if err := c.get(ctx, "/api/salaries", values, &salary); err != nil {
		return nil, err
	}
	var stats statsData
	if err := c.get(ctx, "/api/salaries/stats", values, &stats); err != nil {
		return nil, err
	}
	if stats.Summary == nil {
		stats.Summary = &struct {
			Count       *int     `json:"category_count"`
			Median      *float64 `json:"category_median"`
			BonusMonths *string  `json:"category_avg_bonus"`
			BonusShare  *float64 `json:"category_has_bonus_share"`
		}{}
	}
	r := &domain.Report{SourceURL: func() string {
		v := q.Values()
		v.Del("sp")
		return Origin + "/salaries/" + q.Specialization.Slug + "?" + v.Encode()
	}(), FetchedAt: time.Now().UTC(), SourceKind: "registered_users_self_reported_salary_survey", Query: map[string]string{"specialization": q.Specialization.Slug, "level": q.Level.String(), "city": q.City.String(), "work_format": q.WorkFormat.String()}, Currency: "RUB", Period: "month", Component: "reported_monthly_salary; annual_bonus_separate", SourceCategoryCount: stats.Summary.Count, SourceCategoryMedian: stats.Summary.Median, SummaryScope: "stats endpoint can aggregate more broadly than selected chart filters; do not merge its counts/median with the histogram", Histogram: []domain.Band{}, TopCompanies: stats.Companies, Evidence: []string{Origin + "/api/salaries?" + values.Encode(), Origin + "/api/salaries/stats?" + values.Encode()}, Limitations: []string{"Self-reported survey, not a confirmed employer hiring budget.", "Gross/net basis, salary observation dates and employment hours are not established by these API responses.", "A monthly employee salary is not an hourly project or contractor rate."}}
	r.Company.Requested = strings.TrimSpace(company)
	r.Company.LookupScope = "only_returned_top_ranking; scope may be broader than selected chart filters"
	r.Company.ReturnedRankingSize = len(stats.Companies)
	r.Company.Matches = []domain.CompanySalary{}
	for _, entry := range stats.Companies {
		if strings.EqualFold(strings.TrimSpace(entry.NameRU), r.Company.Requested) || entry.NameEN != nil && strings.EqualFold(strings.TrimSpace(*entry.NameEN), r.Company.Requested) {
			r.Company.Matches = append(r.Company.Matches, entry)
		}
	}
	if r.Company.Requested != "" && len(r.Company.Matches) == 0 {
		r.Limitations = append(r.Limitations, "Requested company is absent from the returned top ranking; this does not prove there are no reports about it elsewhere.")
	}
	if stats.Summary.Count != nil && *stats.Summary.Count < 0 {
		return nil, fmt.Errorf("source returned negative category count")
	}
	if salary.P25 == nil && salary.P50 == nil && salary.P90 == nil {
		return nil, fmt.Errorf("selected salary chart is unavailable; not interpreted as zero pay or an empty authenticated cohort")
	}
	if salary.P25 == nil || salary.P50 == nil || salary.P90 == nil {
		return nil, fmt.Errorf("selected chart is missing salary percentiles; report unavailable")
	}
	p := domain.Percentiles{P25: *salary.P25 * 1000, P50: *salary.P50 * 1000, P90: *salary.P90 * 1000}
	if p.P25 < 0 || p.P25 > p.P50 || p.P50 > p.P90 {
		return nil, fmt.Errorf("selected salary percentiles are not ordered")
	}
	r.Available = true
	r.Percentiles = &p
	if len(salary.Histogram.X) != len(salary.Histogram.Y) || len(salary.Histogram.X) == 0 {
		return nil, fmt.Errorf("salary histogram boundaries and counts are missing or inconsistent")
	}
	for i, start := range salary.Histogram.X {
		if start < 0 || salary.Histogram.Y[i] < 0 || i > 0 && start <= salary.Histogram.X[i-1] {
			return nil, fmt.Errorf("invalid histogram bin %d", i)
		}
		band := domain.Band{Min: start * 1000, Count: salary.Histogram.Y[i]}
		if i+1 < len(salary.Histogram.X) {
			end := salary.Histogram.X[i+1] * 1000
			band.Max = &end
		}
		r.Histogram = append(r.Histogram, band)
		r.HistogramCount += band.Count
	}
	count := r.HistogramCount
	r.SampleSize = &count
	if stats.Summary.Count != nil && count != *stats.Summary.Count {
		r.Limitations = append(r.Limitations, fmt.Sprintf("Stats category_count=%d differs from selected-chart histogram total=%d; primary sample size uses the histogram only.", *stats.Summary.Count, count))
	}
	if stats.Summary.Median != nil && math.Abs(p.P50-*stats.Summary.Median) > 1 {
		r.Limitations = append(r.Limitations, fmt.Sprintf("Stats market median %.0f differs from selected-chart median %.0f; retain them as separately scoped aggregates.", *stats.Summary.Median, p.P50))
	}

	r.Bonus.RecipientsPercent = stats.Summary.BonusShare
	r.Limitations = append(r.Limitations, "Annual bonus and top-company ranking come from the separately scoped stats response; selected chart percentiles do not include an inferred bonus.")
	if stats.Summary.BonusMonths != nil {
		v, err := strconv.ParseFloat(*stats.Summary.BonusMonths, 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("invalid source annual bonus multiplier %q", *stats.Summary.BonusMonths)
		}
		r.Bonus.AverageMonthlySalaries = &v
	}
	if hours > 0 {
		r.HourlyEquivalent = &struct {
			HoursPerMonth  float64            `json:"hours_per_month"`
			Percentiles    domain.Percentiles `json:"percentiles"`
			Interpretation string             `json:"interpretation"`
		}{hours, domain.Percentiles{P25: p.P25 / hours, P50: p.P50 / hours, P90: p.P90 / hours}, "Arithmetic monthly salary equivalent using explicitly supplied hours, not a contractor quote or recommended ask."}
	}
	return r, nil
}
