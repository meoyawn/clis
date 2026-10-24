package service

import "fmt"

type ReviewSort struct{ value string }

const (
	usefulSortValue = "usefulness_desc"
	highSortValue   = "score_desc"
	lowSortValue    = "score_asc"
)

var allowedReviewSortValues = [...]string{usefulSortValue, highSortValue, lowSortValue}

func UsefulReviewSort() ReviewSort { return ReviewSort{usefulSortValue} }
func HighReviewSort() ReviewSort   { return ReviewSort{highSortValue} }
func LowReviewSort() ReviewSort    { return ReviewSort{lowSortValue} }
func NewReviewSortFromValue(v string) (ReviewSort, error) {
	switch v {
	case usefulSortValue:
		return UsefulReviewSort(), nil
	case highSortValue:
		return HighReviewSort(), nil
	case lowSortValue:
		return LowReviewSort(), nil
	}
	return ReviewSort{}, fmt.Errorf("invalid review sort %q; allowed values: %v", v, allowedReviewSortValues)
}
func (s ReviewSort) String() string { return s.value }

type ReviewScope struct{ value string }

const (
	allScopeValue = "all"
	skuScopeValue = "sku"
)

var allowedReviewScopeValues = [...]string{"all", "sku"}

func AllReviewScope() ReviewScope    { return ReviewScope{allScopeValue} }
func SKUReviewScope() ReviewScope    { return ReviewScope{skuScopeValue} }
func (s ReviewScope) String() string { return s.value }
func (s ReviewScope) parameter() string {
	if s.value == skuScopeValue {
		return "1"
	}
	return "2"
}
func NewReviewScopeFromValue(v string) (ReviewScope, error) {
	switch v {
	case allScopeValue:
		return AllReviewScope(), nil
	case skuScopeValue:
		return SKUReviewScope(), nil
	}
	return ReviewScope{}, fmt.Errorf("invalid review scope %q; allowed values: %v", v, allowedReviewScopeValues)
}
