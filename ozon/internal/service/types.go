package service

import "time"

type Metadata struct {
	HTTPRequests    int       `json:"http_requests"`
	FetchedAt       time.Time `json:"fetched_at"`
	SourceURLs      []string  `json:"source_urls"`
	IsAuthenticated *bool     `json:"is_authenticated,omitempty"`
	RetryAfter      string    `json:"retry_after,omitempty"`
	Warnings        []string  `json:"warnings,omitempty"`
}
type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Money struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Text     string  `json:"text"`
}
type Prices struct {
	WithOzonCard    *Money `json:"with_ozon_card"`
	WithoutOzonCard *Money `json:"without_ozon_card"`
	Original        *Money `json:"original,omitempty"`
	CardLabel       string `json:"card_label,omitempty"`
	RegularLabel    string `json:"regular_label,omitempty"`
}
type Pagination struct {
	Page       int    `json:"page"`
	Count      int    `json:"count"`
	Total      *int64 `json:"total"`
	HasMore    bool   `json:"has_more"`
	NextPage   *int   `json:"next_page"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type Product struct {
	SKU              string   `json:"sku"`
	URL              string   `json:"url"`
	Title            string   `json:"title"`
	Images           []string `json:"images"`
	Rating           *float64 `json:"rating"`
	NumRatings       *int64   `json:"num_ratings"`
	Price            *Money   `json:"price"`
	Quantity         *int64   `json:"quantity"`
	QuantitySource   string   `json:"quantity_source,omitempty"`
	MaxOrderQuantity *int64   `json:"max_order_quantity,omitempty"`
	DeliveryDay      string   `json:"delivery_day"`
}
type SearchResult struct {
	Metadata
	Pagination
	*SearchFacets
	Query         string    `json:"query"`
	Results       []Product `json:"results"`
	ExcludedCount int       `json:"excluded_count,omitempty"`
	Message       string    `json:"message,omitempty"`
}

// A nil SearchFacets omits both fields. Nil slices mean the requested facets
// were unavailable on this page; empty slices mean no options were returned.
type SearchFacets struct {
	Filters []Filter       `json:"filters"`
	Sorting []FilterOption `json:"sorting"`
}
type Attribute struct {
	Key    string   `json:"key,omitempty"`
	Name   string   `json:"name"`
	Values []string `json:"values"`
	Group  string   `json:"group,omitempty"`
}
type Variant struct {
	SKU          string `json:"sku"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Value        string `json:"value"`
	Selected     bool   `json:"selected"`
	Availability string `json:"availability"`
	Image        string `json:"image"`
	Price        *Money `json:"price"`
}
type VariantGroup struct {
	Key      string    `json:"key"`
	Name     string    `json:"name"`
	Variants []Variant `json:"variants"`
}
type Seller struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	SKU          string   `json:"sku"`
	ProductURL   string   `json:"product_url"`
	Prices       Prices   `json:"prices"`
	DeliveryDay  string   `json:"delivery_day"`
	Rating       *float64 `json:"rating"`
	Current      bool     `json:"current"`
	SeenIn       []string `json:"seen_in"`
	PriceRank    *int     `json:"price_rank,omitempty"`
	DeliveryRank *int     `json:"delivery_rank,omitempty"`
}
type ShowResult struct {
	Metadata
	Product
	Full              bool           `json:"full"`
	Prices            Prices         `json:"prices"`
	Available         *bool          `json:"available"`
	Variants          []VariantGroup `json:"variants"`
	Sellers           []Seller       `json:"sellers"`
	SellersTruncated  bool           `json:"sellers_truncated"`
	Description       string         `json:"description"`
	DescriptionImages []string       `json:"description_images"`
	Characteristics   []Attribute    `json:"characteristics"`
}
type FilterOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	URL      string `json:"url,omitempty"`
	Image    string `json:"image,omitempty"`
}
type Filter struct {
	Key              string         `json:"key"`
	Type             string         `json:"type"`
	Title            string         `json:"title"`
	Description      string         `json:"description,omitempty"`
	Selected         bool           `json:"selected"`
	Multiple         bool           `json:"multiple"`
	Min              *float64       `json:"min,omitempty"`
	Max              *float64       `json:"max,omitempty"`
	Options          []FilterOption `json:"options"`
	OptionsTruncated bool           `json:"options_truncated"`
}
type FiltersResult struct {
	Metadata
	Query   string         `json:"query"`
	Filters []Filter       `json:"filters"`
	Sorting []FilterOption `json:"sorting"`
	Message string         `json:"message,omitempty"`
}

// Media keeps the original server order within each type. URLs are returned,
// never downloaded or expanded via per-media calls.
type Media struct {
	Type            string `json:"type"`
	ID              string `json:"id,omitempty"`
	URL             string `json:"url"`
	PreviewURL      string `json:"preview_url,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	DurationSeconds *int   `json:"duration_seconds,omitempty"`
}
type Review struct {
	ID               string      `json:"id"`
	SKU              string      `json:"sku"`
	URL              string      `json:"url"`
	Author           string      `json:"author"`
	Rating           *float64    `json:"rating"`
	PublishedAt      *time.Time  `json:"published_at"`
	UpdatedAt        *time.Time  `json:"updated_at,omitempty"`
	Text             string      `json:"text"`
	Pros             string      `json:"pros"`
	Cons             string      `json:"cons"`
	Media            []Media     `json:"media"`
	Variant          []Attribute `json:"variant"`
	VerifiedPurchase *bool       `json:"verified_purchase"`
	Helpful          int64       `json:"helpful"`
	Unhelpful        int64       `json:"unhelpful"`
	CommentsCount    int64       `json:"comments_count"`
}
type ReviewsResult struct {
	Metadata
	Pagination
	SKU              string   `json:"sku"`
	Rating           *float64 `json:"rating"`
	Reviews          []Review `json:"reviews"`
	Scope            string   `json:"scope"`
	Sort             string   `json:"sort"`
	AllVariantsTotal *int64   `json:"all_variants_total"`
}
