package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/meoyawn/clis/levels-fyi/internal/domain"
)

const Origin = "https://api.levels.fyi"
const Website = "https://www.levels.fyi"

var errExpiredToken = errors.New("saved Levels.fyi idToken is expired")

type Client struct {
	http    *http.Client
	headers http.Header
}

func NewClient(client *http.Client, headers http.Header) (*Client, error) {
	if headers == nil || headers.Get("Authorization") == "" {
		return nil, fmt.Errorf("no Levels.fyi authorization: import the selected Chrome session")
	}
	return &Client{client, headers.Clone()}, nil
}

// The selected Cognito account's idToken is the website's actual API bearer.
// No refresh request or account/profile endpoint is needed to import it.
func authUser(s *Session) string {
	const prefix = "CognitoIdentityServiceProvider.76of0ich18hd8uehju7fniv2u1."
	for _, c := range s.Cookies {
		if c.Name == prefix+"LastAuthUser" {
			return c.Value
		}
	}
	return ""
}

func Authorize(s *Session) error {
	const prefix = "CognitoIdentityServiceProvider.76of0ich18hd8uehju7fniv2u1."
	user := authUser(s)
	if user == "" {
		return fmt.Errorf("selected Chrome profile has no active Levels.fyi Cognito account")
	}
	var token string
	for _, c := range s.Cookies {
		if c.Name == prefix+user+".idToken" {
			token = c.Value
		}
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return fmt.Errorf("selected Chrome account has no valid idToken; sign in there and import again")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("invalid Chrome idToken")
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return fmt.Errorf("chrome idToken has invalid expiry metadata; sign in there and import again")
	}
	if claims.Exp <= time.Now().Unix() {
		return fmt.Errorf("%w; open Levels.fyi in the selected Chrome profile to renew its session", errExpiredToken)
	}
	if s.Headers == nil {
		s.Headers = http.Header{}
	}
	s.Headers.Set("Authorization", "Bearer "+token)
	s.Headers.Set("X-Agent", "levelsfyi_website")
	return nil
}

func (c *Client) get(ctx context.Context, path string, values url.Values, out any) error {
	endpoint := Origin + path + "?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", Website+"/")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("read %s: %w; not retried", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("read %s: HTTP %d, Retry-After=%q; stopped without retry", path, res.StatusCode, res.Header.Get("Retry-After"))
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("read %s: non-JSON access/authentication response; stopped", path)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("native response exceeds 8 MiB")
	}
	return decodeWire(data, out)
}

type nativeRow struct {
	YearsRange           string   `json:"-"`
	Masked               []string `json:"-"`
	ID                   string   `json:"uuid"`
	Company              string   `json:"company"`
	CompanySlug          string   `json:"companySlug"`
	Location             string   `json:"location"`
	CountryID            int      `json:"countryId"`
	Family               string   `json:"jobFamilySlug"`
	Level                string   `json:"level"`
	Focus                string   `json:"focusTag"`
	Years                float64  `json:"yearsOfExperience"`
	WorkArrangement      string   `json:"workArrangement"`
	Basis                string   `json:"salaryFormat"`
	EmploymentType       string   `json:"employmentType"`
	OfferDate            string   `json:"offerDate"`
	Base                 *float64 `json:"baseSalary"`
	Stock                *float64 `json:"avgAnnualStockGrantValue"`
	Bonus                *float64 `json:"avgAnnualBonusValue"`
	Total                *float64 `json:"totalCompensation"`
	OriginalBaseCurrency string   `json:"baseSalaryCurrency"`
	ExchangeRate         *float64 `json:"exchangeRate"`
	Hidden               bool     `json:"hidden"`
	Redacted             bool     `json:"isRedacted"`
}

// False scalar fields are the native per-field anonymity marker, distinct
// from account-level hidden/isRedacted access gates. Keep suppressed data null.
func (r *nativeRow) UnmarshalJSON(data []byte) error {
	type alias nativeRow
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	var masked []string
	maskable := map[string]bool{"company": true, "companySlug": true, "location": true, "countryId": true, "jobFamilySlug": true, "level": true, "focusTag": true, "yearsOfExperience": true, "workArrangement": true, "salaryFormat": true, "employmentType": true, "offerDate": true, "baseSalary": true, "avgAnnualStockGrantValue": true, "avgAnnualBonusValue": true, "totalCompensation": true, "baseSalaryCurrency": true, "exchangeRate": true}
	for key, value := range fields {
		if string(value) == "false" && maskable[key] {
			fields[key] = json.RawMessage("null")
			masked = append(masked, key)
		}
	}
	var yearsRange string
	if raw := fields["yearsOfExperience"]; len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		number, err := strconv.ParseFloat(value, 64)
		if err == nil {
			fields["yearsOfExperience"], err = json.Marshal(number)
			if err != nil {
				return err
			}
		} else {
			yearsRange = value
			fields["yearsOfExperience"] = json.RawMessage("null")
		}
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var decoded alias
	if err = json.Unmarshal(normalized, &decoded); err != nil {
		return err
	}
	*r = nativeRow(decoded)
	sort.Strings(masked)
	r.Masked = masked
	r.YearsRange = yearsRange
	return nil
}
func (r nativeRow) masked(field string) bool {
	for _, v := range r.Masked {
		if v == field {
			return true
		}
	}
	return false
}
func text(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
func country(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}

type searchPage struct {
	Total  *int        `json:"total"`
	Hidden *int        `json:"hidden"`
	Rows   []nativeRow `json:"rows"`
}

func (c *Client) Estimate(ctx context.Context, q domain.Query, since time.Time) (*domain.Report, error) {
	if _, err := domain.NewLevelFromValue(q.Level.String()); err != nil {
		return nil, err
	}
	if q.JobFamily == "" || q.Limit < 1 || q.Limit > 50 || q.MaxPages < 1 || q.MaxPages > 20 {
		return nil, fmt.Errorf("invalid job family or pagination (limit 1..50, max-pages 1..20)")
	}
	for _, id := range q.CountryIDs {
		if id <= 0 {
			return nil, fmt.Errorf("invalid country ID %d", id)
		}
	}
	values := url.Values{"jobFamilySlug": {q.JobFamily}, "currency": {"USD"}, "limit": {strconv.Itoa(q.Limit)}, "sortBy": {"offer_date"}, "sortOrder": {"DESC"}}
	if q.Role != "software-engineer" && q.Role != "all" {
		values.Set("searchText", q.Role)
	}
	if q.Remote {
		values.Set("workArrangement", "remote")
	}
	minYears, maxYears := q.Level.Years()
	if minYears > 0 {
		values.Set("minYearsOfExp", strconv.Itoa(minYears))
	}
	if maxYears != nil {
		values.Set("maxYearsOfExp", strconv.Itoa(*maxYears))
	}
	for _, id := range q.CountryIDs {
		values.Add("countryIds[]", strconv.Itoa(id))
	}
	if q.Company != "" {
		values.Set("companySlug", q.Company)
	}
	if !since.IsZero() {
		values.Set("sinceDate", since.UTC().Format(time.RFC3339Nano))
	}
	r := &domain.Report{SourceURL: Website + "/t/" + url.PathEscape(q.JobFamily), SourceKind: "recent_self_reported_submissions_sample", FetchedAt: time.Now().UTC(), Query: q, LevelFilter: q.Level.String(), Geography: "all countries available to this account; no country restriction", Submissions: []domain.Submission{}, Evidence: []string{}, Limitations: []string{
		"Retrieved submissions are a recent, self-selected sample, not representative world-market percentiles or an employer hiring budget.",
		"Source search totals may be capped (the website exposes at most 250 recent matches in the inspected account); source_reported_total is not the size of the full salary database.",
		"Native search amounts are normalized to annual USD; original currency metadata and source exchange rate are retained separately. The source does not date its FX rate.",
		"Level filters mean native experience bands (senior = 7+ years), not a standardized job title or a company's level.",
		"searchText is the website's broad text search over cities/tags/etc, not a verified programming-language classification.",
	}}
	if len(q.CountryIDs) > 0 {
		r.Geography = "explicit country IDs only"
	}
	ids := map[string]bool{}
	for page := 0; page < q.MaxPages; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
			values.Set("offset", strconv.Itoa(page*q.Limit))
		}
		var data searchPage
		if err := c.get(ctx, "/v3/salary/search", values, &data); err != nil {
			return nil, err
		}
		r.Requests++
		r.Evidence = append(r.Evidence, Origin+"/v3/salary/search?"+values.Encode())
		if data.Total == nil || *data.Total < 0 || data.Hidden == nil {
			return nil, fmt.Errorf("native search response lacks coverage metadata; stopped")
		}
		if *data.Hidden > 0 {
			return nil, fmt.Errorf("source reports hidden salary rows; complete normal website access before estimating")
		}
		if page == 0 {
			r.SourceReportedTotal = *data.Total
		}
		for _, row := range data.Rows {
			if row.Hidden || row.Redacted {
				return nil, fmt.Errorf("source redacts salary rows; stopped without bypassing access restrictions")
			}
			if row.ID == "" || row.Family != q.JobFamily || q.Remote && row.WorkArrangement != "" && row.WorkArrangement != "remote" || !row.masked("yearsOfExperience") && row.YearsRange == "" && row.Years < float64(minYears) || maxYears != nil && row.YearsRange == "" && row.Years > float64(*maxYears) {
				return nil, fmt.Errorf("source row differs from requested family/experience/remote scope")
			}
			if len(q.CountryIDs) > 0 && !row.masked("countryId") {
				found := false
				for _, id := range q.CountryIDs {
					if row.CountryID == id {
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("source row is outside requested countries")
				}
			}
			if q.Company != "" && !row.masked("companySlug") && row.CompanySlug != q.Company {
				return nil, fmt.Errorf("source row belongs to a different employer")
			}
			if ids[row.ID] {
				continue
			}
			ids[row.ID] = true
			base, err := money(row.Base)
			if err != nil {
				return nil, err
			}
			stock, err := money(row.Stock)
			if err != nil {
				return nil, err
			}
			bonus, err := money(row.Bonus)
			if err != nil {
				return nil, err
			}
			total, err := money(row.Total)
			if err != nil {
				return nil, err
			}
			r.Submissions = append(r.Submissions, domain.Submission{ID: row.ID, Company: text(row.Company), Location: text(row.Location), CountryID: country(row.CountryID), Level: text(row.Level), Focus: text(row.Focus), Years: func() *float64 {
				if row.masked("yearsOfExperience") || row.YearsRange != "" {
					return nil
				}
				return &row.Years
			}(), AnonymizedFields: row.Masked, YearsRange: row.YearsRange, WorkArrangement: row.WorkArrangement, Basis: row.Basis, EmploymentType: row.EmploymentType, OfferDate: row.OfferDate, Base: base, Stock: stock, Bonus: bonus, Total: total, OriginalBaseCurrency: row.OriginalBaseCurrency, SourceExchangeRate: row.ExchangeRate})
		}
		if len(data.Rows) == 0 || (page+1)*q.Limit >= *data.Total {
			break
		}
	}
	r.RetrievedCount = len(r.Submissions)
	countrySet := map[int]bool{}
	for _, v := range r.Submissions {
		if v.CountryID != nil {
			countrySet[*v.CountryID] = true
		}
	}
	for id := range countrySet {
		r.CountryCoverage = append(r.CountryCoverage, id)
	}
	sort.Ints(r.CountryCoverage)
	r.Groups = groups(r.Submissions, false)
	r.Countries = groups(r.Submissions, true)
	if r.RetrievedCount < r.SourceReportedTotal {
		r.Limitations = append(r.Limitations, "Returned fewer rows than the source-reported total; pagination limits, unavailable rows or concurrent changes may limit coverage.")
	}
	if r.RetrievedCount < 20 {
		r.Limitations = append(r.Limitations, "Fewer than 20 retrieved submissions: evidence is too sparse for a confident market estimate.")
	}
	return r, nil
}
func money(v *float64) (*domain.Money, error) {
	if v == nil {
		return nil, nil
	}
	if *v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil, fmt.Errorf("source returned invalid pay amount")
	}
	return &domain.Money{Amount: *v, Currency: "USD", Interval: "year"}, nil
}
func groups(rows []domain.Submission, byCountry bool) []domain.Group {
	buckets := map[string][]domain.Submission{}
	for _, v := range rows {
		key := v.Basis + ":" + v.EmploymentType
		if byCountry {
			id := 0
			if v.CountryID != nil {
				id = *v.CountryID
			}
			key = strconv.Itoa(id) + ":" + key
		}
		buckets[key] = append(buckets[key], v)
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []domain.Group{}
	for _, key := range keys {
		list := buckets[key]
		g := domain.Group{Basis: list[0].Basis, EmploymentType: list[0].EmploymentType, Count: len(list)}
		if byCountry {
			g.CountryID = list[0].CountryID
		}
		base, stock, bonus, total := []float64{}, []float64{}, []float64{}, []float64{}
		for _, r := range list {
			if r.Base != nil {
				base = append(base, r.Base.Amount)
			}
			if r.Stock != nil {
				stock = append(stock, r.Stock.Amount)
			}
			if r.Bonus != nil {
				bonus = append(bonus, r.Bonus.Amount)
			}
			if r.Total != nil {
				total = append(total, r.Total.Amount)
			}
		}
		g.Base = distribution(base)
		g.Stock = distribution(stock)
		g.Bonus = distribution(bonus)
		g.Total = distribution(total)
		out = append(out, g)
	}
	return out
}
func distribution(values []float64) *domain.Distribution {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	quantile := func(p float64) domain.Money {
		index := float64(len(values)-1) * p
		lo := int(math.Floor(index))
		hi := int(math.Ceil(index))
		amount := values[lo] + (values[hi]-values[lo])*(index-float64(lo))
		return domain.Money{Amount: amount, Currency: "USD", Interval: "year"}
	}
	return &domain.Distribution{Count: len(values), P25: quantile(.25), P50: quantile(.5), P75: quantile(.75), P90: quantile(.9)}
}

type Location struct {
	ID        string `json:"id"`
	CountryID string `json:"countryId"`
	Name      string `json:"name"`
}

func (c *Client) Locations(ctx context.Context, term string) ([]Location, error) {
	if strings.TrimSpace(term) == "" {
		return nil, fmt.Errorf("location query is empty")
	}
	var values []Location
	err := c.get(ctx, "/geo/autocompleteCity", url.Values{"q": {term}}, &values)
	return values, err
}
