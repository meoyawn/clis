package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/meoyawn/clis/glassdoor/internal/domain"
)

const Origin = "https://www.glassdoor.com"

type Client struct {
	http    *http.Client
	headers http.Header
}

func NewClient(client *http.Client, headers http.Header) *Client {
	if headers == nil {
		headers = http.Header{}
	}
	return &Client{client, headers.Clone()}
}

func (c *Client) do(req *http.Request, result any) error {
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", Origin+"/Salaries/index.htm")
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s after %s: %w; not retried", req.Method, req.URL.Redacted(), time.Since(started).Round(time.Millisecond), err)
	}
	defer res.Body.Close()
	data, err := readResponse(res)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Redacted(), err)
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("%s %s returned %q instead of JSON; authentication/access challenge, stopped", req.Method, req.URL.Redacted(), res.Header.Get("Content-Type"))
	}
	if err = json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode salary API response: %w", err)
	}
	return nil
}
func (c *Client) Titles(ctx context.Context, term string) ([]domain.Title, error) {
	if strings.TrimSpace(term) == "" {
		return nil, fmt.Errorf("title query is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Origin+"/autocomplete/jobTitle?"+url.Values{"term": {term}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var out []domain.Title
	err = c.do(req, &out)
	return out, err
}
func (c *Client) Locations(ctx context.Context, term string) ([]domain.Location, error) {
	if strings.TrimSpace(term) == "" {
		return nil, fmt.Errorf("location query is empty")
	}
	values := url.Values{"term": {term}, "locationTypeFilters": {"CITY", "STATE", "COUNTRY", "POSTALCODE"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Origin+"/autocomplete/location?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var out []domain.Location
	err = c.do(req, &out)
	return out, err
}

func (c *Client) Employers(ctx context.Context, term string) ([]domain.Employer, error) {
	if strings.TrimSpace(term) == "" {
		return nil, fmt.Errorf("employer query is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Origin+"/autocomplete/employers?"+url.Values{"term": {term}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	var out []domain.Employer
	err = c.do(req, &out)
	return out, err
}

type payPoint struct {
	Percentile string  `json:"percentile"`
	Value      float64 `json:"value"`
}
type salaryData struct {
	Base       []payPoint `json:"basePayPercentiles"`
	Additional []payPoint `json:"additionalPayPercentiles"`
	Total      []payPoint `json:"totalPayPercentiles"`
	Confidence string     `json:"confidence"`
	Currency   struct {
		Code string `json:"code"`
	} `json:"currency"`
	UpdatedAt string `json:"estimateSourceUpdateTime"`
	JobTitle  struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
	} `json:"jobTitle"`
	PayPeriod string `json:"payPeriod"`
	Location  struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"queryLocation"`
	Count *int `json:"salariesCount"`
}

func (c *Client) Estimate(ctx context.Context, q domain.Query) (*domain.Report, error) {
	if _, err := domain.NewExperienceFromValue(q.Experience.String()); err != nil {
		return nil, err
	}
	if q.Title.ID <= 0 || q.Title.JobTitle == "" || q.Location.LocationID <= 0 || q.Industry.ID < 0 {
		return nil, fmt.Errorf("title, location and industry must be resolved from native catalogs")
	}
	input := map[string]any{"jobTitle": q.Title.JobTitle, "industryId": q.Industry.ID, "yearsOfExperience": q.Experience.String()}
	var locationType string
	switch q.Location.Type {
	case "N":
		input["countryId"] = q.Location.LocationID
		locationType = "COUNTRY"
	case "C":
		input["cityId"] = q.Location.LocationID
		locationType = "CITY"
	case "S":
		input["stateId"] = q.Location.LocationID
		locationType = "STATE"
	default:
		return nil, fmt.Errorf("location %q has unsupported native type %q; use a verified country/city/state result", q.Location.Name, q.Location.Type)
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	endpoint := Origin + "/bff/employer-profile-mono/occ-salary-estimates"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	var response struct {
		Data struct {
			Salary *salaryData `json:"occSalaryEstimates"`
		} `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err = c.do(req, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) > 0 && string(response.Errors) != "null" && string(response.Errors) != "[]" {
		return nil, fmt.Errorf("salary API returned errors; no estimate produced")
	}
	d := response.Data.Salary
	if d == nil {
		return nil, fmt.Errorf("salary estimate missing; not interpreted as zero pay or a successful empty result")
	}
	if d.JobTitle.ID != q.Title.ID || !strings.EqualFold(d.JobTitle.Text, q.Title.JobTitle) || d.Location.ID != q.Location.LocationID || d.Location.Type != locationType {
		return nil, fmt.Errorf("salary response title/location differs from the requested cohort; stopped")
	}
	if d.Count == nil || *d.Count < 0 {
		return nil, fmt.Errorf("salary report has no valid sample size")
	}
	period := map[string]string{"MONTHLY": "month", "ANNUAL": "year", "HOURLY": "hour", "WEEKLY": "week"}[d.PayPeriod]
	if period == "" || d.Currency.Code == "" {
		return nil, fmt.Errorf("salary currency/pay period is missing or unsupported (%q); do not guess units", d.PayPeriod)
	}
	interval, err := domain.NewIntervalFromValue(period)
	if err != nil {
		return nil, err
	}
	r := &domain.Report{FetchedAt: time.Now().UTC(), SourceURL: salaryURL(q.Title.JobTitle, q.Location), Title: q.Title, Location: q.Location, Experience: q.Experience.String(), Industry: q.Industry, Currency: d.Currency.Code, Period: period, NativePayPeriod: d.PayPeriod, SalaryCount: *d.Count, ProviderConfidence: d.Confidence, SourceUpdatedAt: d.UpdatedAt}
	if r.BasePay, err = parsePay(d.Base, d.Currency.Code, interval); err != nil {
		return nil, err
	}
	if r.AdditionalPay, err = parsePay(d.Additional, d.Currency.Code, interval); err != nil {
		return nil, err
	}
	if r.TotalPay, err = parsePay(d.Total, d.Currency.Code, interval); err != nil {
		return nil, err
	}
	r.Available = r.TotalPay.P50 != nil && r.BasePay.P50 != nil
	if r.SalaryCount > 0 && !r.Available {
		return nil, fmt.Errorf("nonempty salary cohort has no base/total median")
	}
	return r, nil
}

func parsePay(points []payPoint, currency string, interval domain.Interval) (domain.Percentiles, error) {
	var out domain.Percentiles
	seen := map[string]bool{}
	for _, p := range points {
		if p.Value < 0 || math.IsNaN(p.Value) || math.IsInf(p.Value, 0) || seen[p.Percentile] {
			return out, fmt.Errorf("source pay percentile %q is invalid or duplicated", p.Percentile)
		}
		seen[p.Percentile] = true
		v := domain.Money{Amount: p.Value, Currency: currency, Interval: interval}
		switch p.Percentile {
		case "P_25TH":
			out.P25 = &v
		case "P_50TH":
			out.P50 = &v
		case "P_75TH":
			out.P75 = &v
		case "P_90TH":
			out.P90 = &v
		}
	}
	var prev *domain.Money
	for _, v := range []*domain.Money{out.P25, out.P50, out.P75, out.P90} {
		if v != nil {
			if prev != nil && v.Amount < prev.Amount {
				return out, fmt.Errorf("source salary percentiles are not ordered")
			}
			prev = v
		}
	}
	return out, nil
}

func salaryURL(title string, location domain.Location) string {
	slug := func(s string) string {
		return strings.ReplaceAll(url.PathEscape(strings.ToLower(strings.TrimSpace(s))), "%20", "-")
	}
	name := location.Name
	prefix := "IN"
	if location.Type == "C" {
		name = location.Name + " (" + location.CountryName + ")"
		prefix = "IC"
	}
	if location.Type == "S" {
		prefix = "IS"
	}
	start := len([]rune(name)) + 1
	end := start + len([]rune(title))
	return fmt.Sprintf("%s/Salaries/%s-%s-salary-SRCH_IL.0,%d_%s%d_KO%d,%d.htm", Origin, slug(name), slug(title), len([]rune(name)), prefix, location.LocationID, start, end)
}
