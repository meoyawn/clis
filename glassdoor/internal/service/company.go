package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/meoyawn/clis/glassdoor/internal/domain"
)

type aggregatePay struct {
	Percentiles []struct {
		Ident string  `json:"ident"`
		Value float64 `json:"value"`
	} `json:"percentiles"`
}

func (p aggregatePay) parse(currency string, interval domain.Interval) (domain.Percentiles, error) {
	points := []payPoint{}
	for _, v := range p.Percentiles {
		switch v.Ident {
		case "P25", "P50", "P75", "P90":
			points = append(points, payPoint{Percentile: "P_" + strings.TrimPrefix(v.Ident, "P") + "TH", Value: v.Value})
		}
	}
	return parsePay(points, currency, interval)
}

// CompanySalaries uses the employer table's observed read-only POST. Unlike the
// website's default page, this request always supplies an explicit location.
func (c *Client) CompanySalaries(ctx context.Context, q domain.CompanyQuery) (*domain.CompanyReport, error) {
	if q.Employer.ID <= 0 || q.Employer.Name == "" || q.Location.LocationID <= 0 || q.Location.Name == "" || q.Page < 1 {
		return nil, fmt.Errorf("invalid company salary query: employer=%d location=%d page=%d; resolve native catalogs first", q.Employer.ID, q.Location.LocationID, q.Page)
	}
	period := map[string]string{"year": "ANNUAL", "month": "MONTHLY", "hour": "HOURLY"}[q.Period.String()]
	if period == "" {
		return nil, fmt.Errorf("unsupported company pay period %q: use year, month or hour", q.Period.String())
	}
	locationKey := map[string]string{"N": "countryId", "C": "cityId", "S": "stateId"}[q.Location.Type]
	if locationKey == "" {
		return nil, fmt.Errorf("unsupported company location type %q", q.Location.Type)
	}
	input := map[string]any{"employerId": q.Employer.ID, locationKey: q.Location.LocationID, "jobTitle": q.Title, "page": q.Page, "pageSize": 10, "payPeriod": period, "sgoc": nil, "sort": "UGC_SALARY_COUNT_DESC"}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Origin+"/bff/employer-profile-mono/agg-salary-estimates", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Salary *struct {
				JobTitleCount *int `json:"jobTitleCount"`
				NumPages      *int `json:"numPages"`
				Location      struct {
					Name string `json:"name"`
				} `json:"queryLocation"`
				Results []struct {
					Base     aggregatePay `json:"basePayStatistics"`
					Total    aggregatePay `json:"totalPayStatistics"`
					Currency struct {
						Code string `json:"code"`
					} `json:"currency"`
					Title struct {
						ID   int    `json:"id"`
						Text string `json:"text"`
					} `json:"jobTitle"`
					MostRecent string `json:"mostRecent"`
					Count      *int   `json:"salaryCount"`
				} `json:"results"`
			} `json:"aggregatedSalaryEstimates"`
		} `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err = c.do(req, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 && string(response.Errors) != "null" && string(response.Errors) != "[]" {
		return nil, fmt.Errorf("company salary API returned errors")
	}
	d := response.Data.Salary
	if d == nil || d.JobTitleCount == nil || d.NumPages == nil || *d.JobTitleCount < 0 || *d.NumPages < 0 {
		return nil, fmt.Errorf("company salary response has no valid coverage metadata")
	}
	if !strings.EqualFold(d.Location.Name, q.Location.Name) && !strings.EqualFold(d.Location.Name, q.Location.LongName) {
		return nil, fmt.Errorf("company salary response location %q differs from requested %q", d.Location.Name, q.Location.Name)
	}
	slug := regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(q.Employer.Name, "-")
	values := url.Values{"locationName": {q.Location.Name}, "locationId": {fmt.Sprint(q.Location.LocationID)}, "locationType": {q.Location.Type}, "payPeriod": {period}, "baseOrTotal": {"BASE_PAY"}, "jobTitle": {q.Title}, "page": {fmt.Sprint(q.Page)}}
	r := &domain.CompanyReport{FetchedAt: time.Now().UTC(), SourceURL: fmt.Sprintf("%s/Salary/%s-Salaries-E%d.htm?%s", Origin, strings.Trim(slug, "-"), q.Employer.ID, values.Encode()), Query: q, NativeRequest: input, QueryLocation: d.Location.Name, JobTitleCount: *d.JobTitleCount, NumPages: *d.NumPages, Results: []domain.CompanySalary{}, Limitations: []string{
		"Employer and location are native filters. This employer table has no remote-work, worldwide-hiring or years-of-experience filter; verify hiring/pay scope from the exact employer vacancy or policy and retain country data as supporting evidence.",
		"Base and total pay are provider estimates, not an advertised employer budget. Keep each title's report count and last-report date; do not pool title medians into a market percentile.",
		"Pay period is explicitly requested through the native selector. Gross/net basis, contracted hours and employment type are not returned. No FX or hours conversion is applied.",
	}}
	for _, row := range d.Results {
		if row.Title.ID <= 0 || row.Title.Text == "" || row.Count == nil || *row.Count < 0 || row.Currency.Code == "" {
			return nil, fmt.Errorf("company salary row lacks title, report count or currency")
		}
		base, err := row.Base.parse(row.Currency.Code, q.Period)
		if err != nil {
			return nil, err
		}
		total, err := row.Total.parse(row.Currency.Code, q.Period)
		if err != nil {
			return nil, err
		}
		if *row.Count > 0 && (base.P50 == nil || total.P50 == nil) {
			return nil, fmt.Errorf("company salary title %q has reports but no base/total median", row.Title.Text)
		}
		r.Results = append(r.Results, domain.CompanySalary{Title: domain.Title{ID: row.Title.ID, JobTitle: row.Title.Text}, SalaryCount: *row.Count, MostRecent: row.MostRecent, BasePay: base, TotalPay: total})
	}
	return r, nil
}
