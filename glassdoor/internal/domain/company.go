package domain

import "time"

type CompanyQuery struct {
	Employer Employer `json:"employer"`
	Location Location `json:"location"`
	Title    string   `json:"title_search"`
	Period   Interval `json:"period"`
	Page     int      `json:"page"`
}
type CompanySalary struct {
	Title       Title       `json:"job_title"`
	SalaryCount int         `json:"salary_count"`
	MostRecent  string      `json:"most_recent_report"`
	BasePay     Percentiles `json:"base_pay"`
	TotalPay    Percentiles `json:"total_pay"`
}
type CompanyReport struct {
	FetchedAt     time.Time       `json:"fetched_at"`
	SourceURL     string          `json:"source_url"`
	Query         CompanyQuery    `json:"query"`
	QueryLocation string          `json:"source_location"`
	JobTitleCount int             `json:"job_title_count"`
	NumPages      int             `json:"num_pages"`
	Results       []CompanySalary `json:"results"`
}
