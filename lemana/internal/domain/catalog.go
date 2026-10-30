package domain

import (
	"encoding/json"
	"fmt"
)

type Product struct {
	ID                 string           `json:"id"`
	Code               string           `json:"code,omitempty"`
	Article            string           `json:"article,omitempty"`
	APIID              string           `json:"api_id,omitempty"`
	Countries          []string         `json:"country_codes,omitempty"`
	Title              string           `json:"title"`
	URL                string           `json:"url"`
	Brand              string           `json:"brand,omitempty"`
	Price              *float64         `json:"price"`
	Currency           string           `json:"currency"`
	Rating             *float64         `json:"rating"`
	ReviewCount        *int             `json:"review_count"`
	ProductRating      *float64         `json:"product_rating"`
	ProductReviewCount *int             `json:"product_review_count"`
	GroupRating        *float64         `json:"group_rating"`
	GroupReviewCount   *int             `json:"group_review_count"`
	InStock            *bool            `json:"in_stock"`
	Purchasable        *bool            `json:"purchasable"`
	Images             []string         `json:"images"`
	Characteristics    []Characteristic `json:"characteristics"`
	Description        string           `json:"description,omitempty"`
	Delivery           json.RawMessage  `json:"delivery,omitempty"`
	Availability       json.RawMessage  `json:"availability,omitempty"`
	Documents          json.RawMessage  `json:"documents,omitempty"`
	Reviews            json.RawMessage  `json:"reviews,omitempty"`
}
type Characteristic struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type Result struct {
	URL          string          `json:"url"`
	Total        *int            `json:"total"`
	Page         int             `json:"page"`
	PageSize     int             `json:"page_size"`
	HasMore      bool            `json:"has_more"`
	Products     []Product       `json:"products"`
	Filters      json.RawMessage `json:"filters,omitempty"`
	Sorts        json.RawMessage `json:"sorts,omitempty"`
	HTTPRequests int             `json:"http_requests"`
}
type Sort struct{ value string }

const popular = "popular"
const price = "price"
const priceDesc = "price-desc"
const rating = "rating"
const sale = "sale"

func Popular() Sort           { return Sort{popular} }
func Price() Sort             { return Sort{price} }
func PriceDesc() Sort         { return Sort{priceDesc} }
func Rating() Sort            { return Sort{rating} }
func Sale() Sort              { return Sort{sale} }
func (s Sort) String() string { return s.value }

func NewSortFromValue(value string) (Sort, error) {
	switch value {
	case popular:
		return Popular(), nil
	case price:
		return Price(), nil
	case priceDesc:
		return PriceDesc(), nil
	case rating:
		return Rating(), nil
	case sale:
		return Sale(), nil
	}
	return Sort{}, fmt.Errorf("invalid sort %q; use popular, price, price-desc, rating or sale", value)
}
