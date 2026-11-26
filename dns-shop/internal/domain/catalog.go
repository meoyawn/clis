package domain

import "fmt"

type Product struct {
	ID              string           `json:"id"`
	Code            string           `json:"code,omitempty"`
	Title           string           `json:"title"`
	URL             string           `json:"url"`
	Brand           string           `json:"brand,omitempty"`
	Price           *float64         `json:"price"`
	OldPrice        *float64         `json:"old_price,omitempty"`
	Rating          *float64         `json:"rating"`
	ReviewCount     *int             `json:"review_count"`
	Reliability     string           `json:"reliability,omitempty"`
	Availability    string           `json:"availability,omitempty"`
	Images          []string         `json:"images"`
	Characteristics []Characteristic `json:"characteristics"`
	Description     string           `json:"description,omitempty"`
}

type Characteristic struct {
	Group string `json:"group,omitempty"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Result struct {
	URL          string    `json:"url"`
	City         string    `json:"city,omitempty"`
	Category     string    `json:"category,omitempty"`
	Total        *int      `json:"total"`
	Page         int       `json:"page"`
	PageSize     int       `json:"page_size"`
	HasMore      bool      `json:"has_more"`
	Products     []Product `json:"products"`
	Filters      []Filter  `json:"filters,omitempty"`
	HTTPRequests int       `json:"http_requests"`
}

type Details struct {
	Product
	HTTPRequests int `json:"http_requests"`
}

type Filter struct {
	Key      string        `json:"key"`
	Label    string        `json:"label"`
	Type     string        `json:"type"`
	Min      *float64      `json:"min,omitempty"`
	Max      *float64      `json:"max,omitempty"`
	Count    *int          `json:"count,omitempty"`
	Selected any           `json:"selected,omitempty"`
	Values   []FilterValue `json:"values,omitempty"`
}

type FilterValue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Count *int   `json:"count,omitempty"`
}

type Sort struct{ value string }

var sorts = []string{"popular", "price-asc", "price-desc", "new", "opinion", "rating"}

func (s Sort) String() string { return s.value }

func NewSort(value string) (Sort, error) {
	for _, v := range sorts {
		if v == value {
			return Sort{value}, nil
		}
	}
	return Sort{}, fmt.Errorf("invalid sort %q; use popular, price-asc, price-desc, new, opinion or rating", value)
}
