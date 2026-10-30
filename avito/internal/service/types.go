package service

import (
	"fmt"
	"net/url"
	"time"
)

type Browser struct{ value string }

const (
	chromeBrowserValue  = "chrome"
	firefoxBrowserValue = "firefox"
	braveBrowserValue   = "brave"
	edgeBrowserValue    = "edge"
	safariBrowserValue  = "safari"
)

var allowedBrowserValues = [...]string{chromeBrowserValue, firefoxBrowserValue, braveBrowserValue, edgeBrowserValue, safariBrowserValue}

func ChromeBrowser() Browser { return Browser{value: chromeBrowserValue} }

func FirefoxBrowser() Browser { return Browser{value: firefoxBrowserValue} }

func BraveBrowser() Browser { return Browser{value: braveBrowserValue} }

func EdgeBrowser() Browser { return Browser{value: edgeBrowserValue} }

func SafariBrowser() Browser { return Browser{value: safariBrowserValue} }

func NewBrowserFromValue(value string) (Browser, error) {
	switch value {
	case chromeBrowserValue:
		return ChromeBrowser(), nil
	case firefoxBrowserValue:
		return FirefoxBrowser(), nil
	case braveBrowserValue:
		return BraveBrowser(), nil
	case edgeBrowserValue:
		return EdgeBrowser(), nil
	case safariBrowserValue:
		return SafariBrowser(), nil
	default:
		return Browser{}, fmt.Errorf("invalid browser %q; allowed values: %v", value, allowedBrowserValues)
	}
}

func (b Browser) String() string { return b.value }

type SearchOptions struct {
	Query         string
	LocationID    int64
	LocalPriority bool
	CategoryID    int64
	Page          int
	Filters       url.Values
	Sort          Sort
}

type Price struct {
	Amount   *float64 `json:"amount"`
	Text     string   `json:"text"`
	Currency string   `json:"currency,omitempty"`
}

type Parameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Seller struct {
	Name           string   `json:"name,omitempty"`
	Rating         *float64 `json:"rating"`
	NumRatings     *int64   `json:"num_ratings"`
	RatingsSummary string   `json:"ratings_summary,omitempty"`
}

type Listing struct {
	ID          string      `json:"id"`
	URL         string      `json:"url,omitempty"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Price       Price       `json:"price"`
	CategoryID  int64       `json:"category_id,omitempty"`
	LocationID  int64       `json:"location_id,omitempty"`
	City        string      `json:"city,omitempty"`
	Images      []string    `json:"images,omitempty"`
	Parameters  []Parameter `json:"parameters,omitempty"`
	Active      *bool       `json:"active,omitempty"`
	Seller      Seller      `json:"seller"`
}

type Metadata struct {
	SourceURL       string            `json:"source_url"`
	FetchedAt       time.Time         `json:"fetched_at"`
	HTTPRequests    int               `json:"http_requests"`
	IsAuthenticated *bool             `json:"is_authenticated,omitempty"`
	RateHeaders     map[string]string `json:"rate_headers,omitempty"`
	Warnings        []string          `json:"warnings,omitempty"`
}

// SearchContext contains the server's effective selection, including inferred
// category parameters that must be retained when the caller adds a filter.
type SearchContext struct {
	Query          string     `json:"query"`
	LocationID     int64      `json:"location_id"`
	CategoryID     int64      `json:"category_id,omitempty"`
	LocalPriority  bool       `json:"local_priority"`
	AppliedFilters url.Values `json:"applied_filters"`
	Sort           string     `json:"sort,omitempty"`
	SearchArgs     []string   `json:"search_args"`
}

type SearchResult struct {
	Metadata
	SearchContext
	Total           int64     `json:"total"`
	AccessibleTotal *int64    `json:"accessible_total,omitempty"`
	SimilarTotal    *int64    `json:"similar_total,omitempty"`
	Count           int       `json:"count"`
	Page            int       `json:"page"`
	HasMore         *bool     `json:"has_more,omitempty"`
	NextPage        *int      `json:"next_page,omitempty"`
	ExcludedCount   int       `json:"excluded_count,omitempty"`
	Results         []Listing `json:"results"`
}

type ShowResult struct {
	Metadata
	Listing
}
