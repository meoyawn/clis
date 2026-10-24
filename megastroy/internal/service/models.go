package service

import (
	"encoding/json"
	"fmt"
	"time"
)

const Origin = "https://kazan.megastroy.com"

// Sort is a website sort order. Its zero value uses the site's default.
type Sort struct{ value string }

const (
	sortPopular      = "popular"
	sortPrice        = "price"
	sortPriceDesc    = "price-desc"
	sortSale         = "sale"
	sortLatest       = "latest"
	sortStrikedPrice = "striked-price"
)

var sortValues = [...]string{sortPopular, sortPrice, sortPriceDesc, sortSale, sortLatest, sortStrikedPrice}

func Popular() Sort         { return Sort{sortPopular} }
func PriceAscending() Sort  { return Sort{sortPrice} }
func PriceDescending() Sort { return Sort{sortPriceDesc} }
func Sale() Sort            { return Sort{sortSale} }
func Latest() Sort          { return Sort{sortLatest} }
func StrikedPrice() Sort    { return Sort{sortStrikedPrice} }

func NewSortFromValue(value string) (Sort, error) {
	switch value {
	case "", sortPopular:
		return Popular(), nil
	case sortPrice:
		return PriceAscending(), nil
	case sortPriceDesc:
		return PriceDescending(), nil
	case sortSale:
		return Sale(), nil
	case sortLatest:
		return Latest(), nil
	case sortStrikedPrice:
		return StrikedPrice(), nil
	default:
		return Sort{}, fmt.Errorf("invalid sort %q; allowed values: %v", value, sortValues)
	}
}
func (s Sort) String() string               { return s.value }
func (s Sort) MarshalJSON() ([]byte, error) { return json.Marshal(s.value) }

type Metadata struct {
	SourceURL    string    `json:"source_url"`
	FetchedAt    time.Time `json:"fetched_at"`
	HTTPRequests int       `json:"http_requests"`
	Location     string    `json:"location"`
	Warnings     []string  `json:"warnings,omitempty"`
}

type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Text     string  `json:"text"`
}

type Stock struct {
	Address  string   `json:"address"`
	Quantity *float64 `json:"quantity"`
	Unit     string   `json:"unit"`
	Text     string   `json:"text"`
}

type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Product struct {
	ID           string   `json:"id"`
	URL          string   `json:"url"`
	Name         string   `json:"name"`
	Rating       *float64 `json:"rating"`
	ReviewCount  *int     `json:"review_count"`
	Price        *Money   `json:"price"`
	OldPrice     *Money   `json:"old_price"`
	PriceLabel   string   `json:"price_label,omitempty"`
	Unit         string   `json:"unit"`
	Available    *bool    `json:"available"`
	Availability string   `json:"availability"`
	Stores       []Stock  `json:"stores"`
	Images       []string `json:"images"`
	Category     *Link    `json:"category,omitempty"`
	Labels       []string `json:"labels,omitempty"`
}

type Option struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Count    *int   `json:"count"`
	Selected bool   `json:"selected"`
	URL      string `json:"url,omitempty"`
}

type Filter struct {
	Key      string   `json:"key"`
	Title    string   `json:"title"`
	Type     string   `json:"type"`
	Multiple bool     `json:"multiple"`
	Options  []Option `json:"options,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	From     *float64 `json:"from,omitempty"`
	To       *float64 `json:"to,omitempty"`
	FromKey  string   `json:"from_key,omitempty"`
	ToKey    string   `json:"to_key,omitempty"`
}

type Category struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Count    *int   `json:"count"`
	URL      string `json:"url"`
	ParentID string `json:"parent_id,omitempty"`
	Selected bool   `json:"selected"`
}

// Path contains the ancestors followed by the selected category; Children are
// the next choices published on this page. Other branches are not fetched.
type CategoryTree struct {
	Path     []Category `json:"path"`
	Children []Category `json:"children"`
}

type SearchResult struct {
	Metadata
	Query      string        `json:"query"`
	Total      int           `json:"total"`
	Count      int           `json:"count"`
	Page       int           `json:"page"`
	HasMore    bool          `json:"has_more"`
	NextPage   *int          `json:"next_page"`
	NextURL    *string       `json:"next_url"`
	Results    []Product     `json:"results"`
	Filters    *[]Filter     `json:"filters,omitempty"`
	Categories *CategoryTree `json:"categories,omitempty"`
	Sorting    *[]Option     `json:"sorting,omitempty"`
	Message    string        `json:"message,omitempty"`
}

type Characteristic struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Dimensions struct {
	HeightMM   *float64 `json:"height_mm"`
	WidthMM    *float64 `json:"width_mm"`
	DepthMM    *float64 `json:"depth_mm"`
	LengthMM   *float64 `json:"length_mm"`
	DiameterMM *float64 `json:"diameter_mm"`
	// Exact source properties also preserve mounting distances, packaging sizes,
	// and dimensions with ambiguous units; those are never guessed from a name.
	Properties []Characteristic `json:"properties"`
}

type DetailResult struct {
	Metadata
	Product
	Description     string           `json:"description"`
	Characteristics []Characteristic `json:"characteristics"`
	Dimensions      Dimensions       `json:"dimensions"`
	Breadcrumbs     []Link           `json:"breadcrumbs"`
	Documents       []Link           `json:"documents"`
}
