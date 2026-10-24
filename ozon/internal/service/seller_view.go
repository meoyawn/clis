package service

import "fmt"

type SellerView struct{ value string }

const (
	cheaperSellerValue = "price"
	fasterSellerValue  = "date"
)

var allowedSellerViewValues = [...]string{cheaperSellerValue, fasterSellerValue}

func CheaperSellerView() SellerView { return SellerView{cheaperSellerValue} }
func FasterSellerView() SellerView  { return SellerView{fasterSellerValue} }
func (v SellerView) String() string { return v.value }
func NewSellerViewFromValue(value string) (SellerView, error) {
	switch value {
	case cheaperSellerValue:
		return CheaperSellerView(), nil
	case fasterSellerValue:
		return FasterSellerView(), nil
	}
	return SellerView{}, fmt.Errorf("invalid seller view %q; allowed values: %v", value, allowedSellerViewValues)
}
