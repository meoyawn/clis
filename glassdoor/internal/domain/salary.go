package domain

import (
	"fmt"
	"time"
)

type Experience struct{ value string }

const (
	allExperienceValue = "ALL_YEARS_OF_EXPERIENCE"
	lessThanOneValue   = "LESS_THAN_ONE"
	oneToThreeValue    = "ONE_TO_THREE"
	fourToSixValue     = "FOUR_TO_SIX"
	sevenToNineValue   = "SEVEN_TO_NINE"
	tenToFourteenValue = "TEN_TO_FOURTEEN"
	aboveFifteenValue  = "ABOVE_FIFTEEN"
)

var allowedExperienceValues = [...]string{allExperienceValue, lessThanOneValue, oneToThreeValue, fourToSixValue, sevenToNineValue, tenToFourteenValue, aboveFifteenValue}

func AllExperience() Experience { return Experience{allExperienceValue} }
func LessThanOne() Experience   { return Experience{lessThanOneValue} }
func OneToThree() Experience    { return Experience{oneToThreeValue} }
func FourToSix() Experience     { return Experience{fourToSixValue} }
func SevenToNine() Experience   { return Experience{sevenToNineValue} }
func TenToFourteen() Experience { return Experience{tenToFourteenValue} }
func AboveFifteen() Experience  { return Experience{aboveFifteenValue} }
func NewExperienceFromValue(v string) (Experience, error) {
	switch v {
	case allExperienceValue, "all":
		return AllExperience(), nil
	case lessThanOneValue, "0-1":
		return LessThanOne(), nil
	case oneToThreeValue, "1-3":
		return OneToThree(), nil
	case fourToSixValue, "4-6":
		return FourToSix(), nil
	case sevenToNineValue, "7-9":
		return SevenToNine(), nil
	case tenToFourteenValue, "10-14":
		return TenToFourteen(), nil
	case aboveFifteenValue, "15+":
		return AboveFifteen(), nil
	}
	return Experience{}, fmt.Errorf("invalid experience %q, allowed native values: %v (aliases all, 0-1, 1-3, 4-6, 7-9, 10-14, 15+)", v, allowedExperienceValues)
}
func (v Experience) String() string { return v.value }

type Title struct {
	ID       int    `json:"id"`
	JobTitle string `json:"jobTitle"`
	Label    string `json:"label"`
}
type Location struct {
	ID          int    `json:"id"`
	LocationID  int    `json:"locationId"`
	Type        string `json:"locationType"`
	Name        string `json:"locationName"`
	LongName    string `json:"longName"`
	CountryName string `json:"countryName"`
	CountryID   int    `json:"countryId"`
	Label       string `json:"label"`
}
type Employer struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Label   string `json:"label"`
	Website string `json:"websiteURL"`
}
type Industry struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}
type Query struct {
	Title      Title
	Location   Location
	Experience Experience
	Industry   Industry
}

type Interval struct{ value string }

const (
	monthValue = "month"
	yearValue  = "year"
	hourValue  = "hour"
	weekValue  = "week"
)

func Month() Interval { return Interval{monthValue} }
func Year() Interval  { return Interval{yearValue} }
func Hour() Interval  { return Interval{hourValue} }
func Week() Interval  { return Interval{weekValue} }
func NewIntervalFromValue(v string) (Interval, error) {
	switch v {
	case monthValue:
		return Month(), nil
	case yearValue:
		return Year(), nil
	case hourValue:
		return Hour(), nil
	case weekValue:
		return Week(), nil
	}
	return Interval{}, fmt.Errorf("unsupported pay interval %q", v)
}
func (i Interval) String() string               { return i.value }
func (i Interval) MarshalText() ([]byte, error) { return []byte(i.value), nil }

type Money struct {
	Amount   float64  `json:"amount"`
	Currency string   `json:"currency"`
	Interval Interval `json:"interval"`
}

type Percentiles struct {
	P25 *Money `json:"p25"`
	P50 *Money `json:"p50"`
	P75 *Money `json:"p75"`
	P90 *Money `json:"p90"`
}
type Report struct {
	FetchedAt               time.Time      `json:"fetched_at"`
	SourceURL               string         `json:"source_url"`
	Endpoint                string         `json:"endpoint"`
	SourceKind              string         `json:"source_kind"`
	Available               bool           `json:"available"`
	Title                   Title          `json:"job_title"`
	Location                Location       `json:"location"`
	Experience              string         `json:"experience"`
	Industry                Industry       `json:"industry"`
	NativeRequest           map[string]any `json:"native_request"`
	Currency                string         `json:"currency"`
	Period                  string         `json:"period"`
	NativePayPeriod         string         `json:"native_pay_period"`
	Basis                   *string        `json:"basis"`
	SalaryCount             int            `json:"salary_count"`
	ProviderConfidence      string         `json:"provider_confidence"`
	SourceUpdatedAt         string         `json:"source_updated_at"`
	BasePay                 Percentiles    `json:"base_pay"`
	AdditionalPay           Percentiles    `json:"additional_pay"`
	TotalPay                Percentiles    `json:"total_pay"`
	EmployerConfirmedBudget any            `json:"employer_confirmed_budget"`
	Limitations             []string       `json:"limitations"`
}
