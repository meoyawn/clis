package domain

import (
	"net/url"
	"time"
)

type Specialization struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Query struct {
	Specialization Specialization
	Level          Level
	City           City
	WorkFormat     WorkFormat
}

func (q Query) Values() url.Values {
	v := url.Values{"sp": {q.Specialization.Slug}}
	if q.Level != AllLevels() {
		v.Set("se", q.Level.String())
	}
	if q.City != AllCities() {
		v.Set("l", q.City.String())
	}
	if q.WorkFormat != AllWorkFormats() {
		v.Set("wf", q.WorkFormat.String())
	}
	return v
}

type Band struct {
	Min   float64  `json:"min"`
	Max   *float64 `json:"max"`
	Count int      `json:"count"`
}

type CompanySalary struct {
	NameRU     string  `json:"name_ru_company"`
	NameEN     *string `json:"name_en_company"`
	Min        float64 `json:"min_salary"`
	Max        float64 `json:"max_salary"`
	Median     float64 `json:"median_salary"`
	SampleSize *int    `json:"sample_size"`
}

type Percentiles struct {
	P25 float64 `json:"p25"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
}

type Report struct {
	SourceURL            string            `json:"source_url"`
	FetchedAt            time.Time         `json:"fetched_at"`
	SourceKind           string            `json:"source_kind"`
	Query                map[string]string `json:"query"`
	Currency             string            `json:"currency"`
	Period               string            `json:"period"`
	Basis                *string           `json:"basis"`
	Component            string            `json:"component"`
	SampleSize           *int              `json:"sample_size"`
	SourceCategoryCount  *int              `json:"source_category_count"`
	SourceCategoryMedian *float64          `json:"source_category_median"`
	SummaryScope         string            `json:"summary_scope"`
	Available            bool              `json:"available"`
	Percentiles          *Percentiles      `json:"percentiles"`
	Histogram            []Band            `json:"histogram"`
	HistogramCount       int               `json:"histogram_count"`
	Bonus                struct {
		RecipientsPercent      *float64 `json:"recipients_percent"`
		AverageMonthlySalaries *float64 `json:"average_monthly_salaries"`
	} `json:"annual_bonus"`
	Company struct {
		Requested               string          `json:"requested"`
		LookupScope             string          `json:"lookup_scope"`
		ReturnedRankingSize     int             `json:"returned_ranking_size"`
		Matches                 []CompanySalary `json:"matches"`
		EmployerConfirmedBudget any             `json:"employer_confirmed_budget"`
	} `json:"company"`
	TopCompanies     []CompanySalary `json:"top_companies"`
	HourlyEquivalent *struct {
		HoursPerMonth  float64     `json:"hours_per_month"`
		Percentiles    Percentiles `json:"percentiles"`
		Interpretation string      `json:"interpretation"`
	} `json:"hourly_equivalent,omitempty"`
	Evidence    []string `json:"evidence"`
	Limitations []string `json:"limitations"`
}
