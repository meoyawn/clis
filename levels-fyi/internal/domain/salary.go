package domain

import (
	"fmt"
	"time"
)

type Level struct{ value string }

const (
	allValue    = "all"
	juniorValue = "junior"
	middleValue = "middle"
	seniorValue = "senior"
)

func AllLevels() Level { return Level{allValue} }
func Junior() Level    { return Level{juniorValue} }
func Middle() Level    { return Level{middleValue} }
func Senior() Level    { return Level{seniorValue} }
func NewLevelFromValue(v string) (Level, error) {
	switch v {
	case allValue:
		return AllLevels(), nil
	case juniorValue:
		return Junior(), nil
	case middleValue:
		return Middle(), nil
	case seniorValue:
		return Senior(), nil
	}
	return Level{}, fmt.Errorf("invalid level %q: use all, junior, middle, senior (native years-of-experience bands)", v)
}
func (l Level) String() string { return l.value }
func (l Level) Years() (int, *int) {
	switch l.value {
	case juniorValue:
		m := 1
		return 0, &m
	case middleValue:
		m := 6
		return 2, &m
	case seniorValue:
		return 7, nil
	}
	return 0, nil
}

type Query struct {
	Role          string `json:"role"`
	JobFamily     string `json:"job_family"`
	Level         Level  `json:"-"`
	Remote        bool   `json:"remote"`
	NewOffersOnly bool   `json:"new_offers_only"`
	CountryIDs    []int  `json:"country_ids"`
	Company       string `json:"company_slug,omitempty"`
	Limit         int    `json:"limit"`
	MaxPages      int    `json:"max_pages"`
}
type EmployerSample struct {
	Company             string  `json:"company_slug"`
	SourceReportedTotal int     `json:"source_reported_total"`
	RetrievedCount      int     `json:"retrieved_count"`
	SelectedCount       int     `json:"selected_count"`
	Groups              []Group `json:"sample_distributions"`
}
type CohortReport struct {
	FetchedAt      time.Time        `json:"fetched_at"`
	Companies      []string         `json:"company_slugs"`
	FocusTags      []string         `json:"exact_focus_tags"`
	Query          Query            `json:"query"`
	LevelFilter    string           `json:"level_filter"`
	Employers      []EmployerSample `json:"by_employer"`
	RetrievedCount int              `json:"retrieved_count"`
	SelectedCount  int              `json:"selected_count"`
	ExcludedCount  int              `json:"excluded_count"`
	Submissions    []Submission     `json:"submissions"`
	Groups         []Group          `json:"sample_distributions"`
	Countries      []Group          `json:"by_country"`
	Evidence       []string         `json:"evidence"`
}
type Money struct {
	Amount float64 `json:"amount"`
}
type Submission struct {
	YearsRange           string   `json:"source_years_range,omitempty"`
	AnonymizedFields     []string `json:"anonymized_fields,omitempty"`
	ID                   string   `json:"source_id"`
	Company              *string  `json:"company"`
	Location             *string  `json:"location"`
	CountryID            *int     `json:"country_id"`
	Level                *string  `json:"level"`
	Focus                *string  `json:"focus"`
	Years                *float64 `json:"years_of_experience"`
	WorkArrangement      string   `json:"work_arrangement"`
	Basis                string   `json:"basis"`
	EmploymentType       string   `json:"employment_type"`
	OfferDate            string   `json:"offer_date"`
	Base                 *Money   `json:"base"`
	Stock                *Money   `json:"stock"`
	Bonus                *Money   `json:"bonus"`
	Total                *Money   `json:"total"`
	OriginalBaseCurrency string   `json:"original_base_currency"`
	SourceExchangeRate   *float64 `json:"source_exchange_rate"`
}
type Distribution struct {
	Count int   `json:"count"`
	P25   Money `json:"p25"`
	P50   Money `json:"p50"`
	P75   Money `json:"p75"`
	P90   Money `json:"p90"`
}
type Group struct {
	Basis          string        `json:"basis"`
	EmploymentType string        `json:"employment_type"`
	CountryID      *int          `json:"country_id,omitempty"`
	Count          int           `json:"count"`
	Base           *Distribution `json:"base"`
	Stock          *Distribution `json:"stock"`
	Bonus          *Distribution `json:"bonus"`
	Total          *Distribution `json:"total"`
}
type Report struct {
	SourceURL           string       `json:"source_url"`
	FetchedAt           time.Time    `json:"fetched_at"`
	Query               Query        `json:"query"`
	LevelFilter         string       `json:"level_filter"`
	SourceReportedTotal int          `json:"source_reported_total"`
	RetrievedCount      int          `json:"retrieved_count"`
	Requests            int          `json:"network_requests"`
	CountryCoverage     []int        `json:"observed_country_ids"`
	Groups              []Group      `json:"sample_distributions"`
	Countries           []Group      `json:"by_country"`
	Submissions         []Submission `json:"submissions"`
	Evidence            []string     `json:"evidence"`
}
