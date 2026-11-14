package domain

import "fmt"

type NegotiationFilter struct{ value string }

const (
	activeNegotiationFilter = "active"
	deletedNegotiationFilter = "deleted"
)

var negotiationFilterValues = [...]string{activeNegotiationFilter, deletedNegotiationFilter}

func ActiveNegotiations() NegotiationFilter {
	return NegotiationFilter{value: activeNegotiationFilter}
}

func DeletedNegotiations() NegotiationFilter {
	return NegotiationFilter{value: deletedNegotiationFilter}
}

func NewNegotiationFilterFromValue(value string) (NegotiationFilter, error) {
	switch value {
	case activeNegotiationFilter, "all":
		return ActiveNegotiations(), nil
	case deletedNegotiationFilter:
		return DeletedNegotiations(), nil
	}
	return NegotiationFilter{}, fmt.Errorf("invalid negotiation filter %q, allowed values: %v (all aliases active)", value, negotiationFilterValues)
}

func (filter NegotiationFilter) String() string { return filter.value }
