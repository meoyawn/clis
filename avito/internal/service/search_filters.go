package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type Sort struct{ value string }

var sortValuePattern = regexp.MustCompile(`^[1-9][0-9]{0,12}(_(asc|desc))?$`)

func NewSortFromValue(value string) (Sort, error) {
	if !sortValuePattern.MatchString(value) {
		return Sort{}, fmt.Errorf("invalid sort %q; use a value from avito filters", value)
	}
	return Sort{value: value}, nil
}

func (s Sort) String() string { return s.value }

var parameterKey = regexp.MustCompile(`^params\[([1-9][0-9]{0,12})\](\[(from|to)\])?$`)
var decimalValue = regexp.MustCompile(`^-?[0-9]{1,15}(\.[0-9]{1,6})?$`)

// These are form IDs, not arbitrary request parameters. Transport, pagination,
// query, and category state cannot be overridden through --filter.
func supportedFilterKey(key string) bool {
	if parameterKey.MatchString(key) {
		return true
	}
	switch key {
	case "pmin", "pmax", "owner", "withDeliveryOnly", "withImagesOnly", "byTitle":
		return true
	default:
		return false
	}
}

func ValidateFilters(filters url.Values) error {
	if len(filters) > 100 {
		return fmt.Errorf("too many filter keys: %d; maximum is 100", len(filters))
	}
	for key, values := range filters {
		if !supportedFilterKey(key) {
			return fmt.Errorf("invalid filter key %q; use keys from avito filters (for example params[123])", key)
		}
		if len(values) == 0 || len(values) > 100 {
			return fmt.Errorf("filter %q needs 1–100 values", key)
		}
		isRange := key == "pmin" || key == "pmax" || strings.HasSuffix(key, "[from]") || strings.HasSuffix(key, "[to]")
		if len(values) > 1 && (!strings.HasPrefix(key, "params[") || isRange) {
			return fmt.Errorf("filter %q must be specified once", key)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("filter %q needs 1–2048 bytes of nonblank text without newlines or NUL", key)
			}
			if seen[value] {
				return fmt.Errorf("filter %q repeats value %q", key, value)
			}
			seen[value] = true
			if isRange && !decimalValue.MatchString(value) {
				return fmt.Errorf("filter %q has invalid numeric bound %q", key, value)
			}
			if (key == "pmin" || key == "pmax") && strings.HasPrefix(value, "-") {
				return fmt.Errorf("filter %q must not be negative: %q", key, value)
			}
			if key == "owner" && value != "0" && value != "1" && value != "2" {
				return fmt.Errorf("filter owner %q must be 0 (all), 1 (private), or 2 (companies)", value)
			}
			if (key == "withDeliveryOnly" || key == "withImagesOnly" || key == "byTitle") && value != "0" && value != "1" {
				return fmt.Errorf("filter %q value %q must be 0 or 1", key, value)
			}
		}
		if isRange {
			base := strings.TrimSuffix(strings.TrimSuffix(key, "[from]"), "[to]")
			if base != key && len(filters[base]) > 0 {
				return fmt.Errorf("filter %q cannot be combined with its range bound %q", base, key)
			}
			fromKey, toKey := base+"[from]", base+"[to]"
			if key == "pmin" || key == "pmax" {
				fromKey, toKey = "pmin", "pmax"
			}
			from, to := filters.Get(fromKey), filters.Get(toKey)
			if decimalValue.MatchString(from) && decimalValue.MatchString(to) {
				low, err := strconv.ParseFloat(from, 64)
				if err != nil {
					return fmt.Errorf("parse filter %q bound %q: %w", fromKey, from, err)
				}
				high, err := strconv.ParseFloat(to, 64)
				if err != nil {
					return fmt.Errorf("parse filter %q bound %q: %w", toKey, to, err)
				}
				if low > high {
					return fmt.Errorf("filter range %q=%q exceeds %q=%q", fromKey, from, toKey, to)
				}
			}
		}
	}
	return nil
}

func ValidateSearchOptions(options SearchOptions) error {
	if err := ValidateSearchQuery(options.Query, options.CategoryID, options.Page); err != nil {
		return err
	}
	return ValidateFilters(options.Filters)
}

func encodeFilters(query, filters url.Values) {
	for key, values := range filters {
		wireKey := key
		switch key {
		case "owner":
			wireKey = "user"
		case "withDeliveryOnly":
			wireKey = "d"
		case "withImagesOnly":
			wireKey = "i"
		case "byTitle":
			wireKey = "bt"
		}
		if len(values) == 1 {
			value := values[0]
			if key == "withDeliveryOnly" || key == "withImagesOnly" || key == "byTitle" {
				if value == "1" {
					value = "on"
				} else {
					value = "off"
				}
			}
			query.Set(wireKey, value)
		} else {
			for i, value := range values {
				query.Set(fmt.Sprintf("%s[%d]", wireKey, i), value)
			}
		}
	}
}

type webSearchCore struct {
	Query            string                     `json:"query"`
	LocationID       int64                      `json:"locationId"`
	LocationName     string                     `json:"locationName"`
	CategoryID       int64                      `json:"categoryId"`
	Page             int                        `json:"page"`
	Params           map[string]json.RawMessage `json:"params"`
	LocalPriority    int                        `json:"localPriority"`
	Sort             string                     `json:"sort"`
	PriceMin         json.RawMessage            `json:"priceMin"`
	PriceMax         json.RawMessage            `json:"priceMax"`
	Owner            json.RawMessage            `json:"owner"`
	WithDeliveryOnly json.RawMessage            `json:"withDeliveryOnly"`
	WithImagesOnly   json.RawMessage            `json:"withImagesOnly"`
	ByTitle          json.RawMessage            `json:"byTitle"`
}

func scalarText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String(), nil
	}
	var boolean bool
	if json.Unmarshal(raw, &boolean) == nil {
		if boolean {
			return "1", nil
		}
		return "0", nil
	}
	return "", fmt.Errorf("expected a string, number, boolean, or null")
}

func selectionValues(raw json.RawMessage) ([]string, error) {
	if len(raw) > 0 && raw[0] == '[' {
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, err
		}
		values := []string{}
		for _, entry := range array {
			value, err := scalarText(entry)
			if err != nil || value == "" {
				return nil, fmt.Errorf("invalid selection array value")
			}
			values = append(values, value)
		}
		return values, nil
	}
	value, err := scalarText(raw)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return []string{}, nil
	}
	return []string{value}, nil
}

func flattenParams(params map[string]json.RawMessage) (url.Values, error) {
	filters := url.Values{}
	for id, raw := range params {
		key := "params[" + id + "]"
		if !parameterKey.MatchString(key) {
			return nil, fmt.Errorf("unsupported parameter ID %q in search response", id)
		}
		if len(raw) > 0 && raw[0] == '{' {
			var bounds map[string]json.RawMessage
			if err := json.Unmarshal(raw, &bounds); err != nil {
				return nil, fmt.Errorf("decode range %q: %w", key, err)
			}
			for bound, data := range bounds {
				if bound != "from" && bound != "to" {
					return nil, fmt.Errorf("unsupported range bound %q for %q", bound, key)
				}
				value, err := scalarText(data)
				if err != nil {
					return nil, fmt.Errorf("decode bound %q for %q: %w", bound, key, err)
				}
				if value != "" {
					filters.Set(key+"["+bound+"]", value)
				}
			}
			continue
		}
		values, err := selectionValues(raw)
		if err != nil {
			return nil, fmt.Errorf("decode selection %q: %w", key, err)
		}
		if len(values) > 0 {
			filters[key] = values
		}
	}
	return filters, nil
}

func (core webSearchCore) context(options SearchOptions) (SearchContext, error) {
	if core.Page != options.Page {
		return SearchContext{}, fmt.Errorf("requested page %d but web search returned page %d", options.Page, core.Page)
	}
	if core.LocationID != options.LocationID {
		return SearchContext{}, fmt.Errorf("requested location %d but web search returned location %d", options.LocationID, core.LocationID)
	}
	if options.CategoryID > 0 && core.CategoryID != options.CategoryID {
		return SearchContext{}, fmt.Errorf("requested category %d but web search returned category %d", options.CategoryID, core.CategoryID)
	}
	filters, err := flattenParams(core.Params)
	if err != nil {
		return SearchContext{}, err
	}
	for key, raw := range map[string]json.RawMessage{
		"pmin": core.PriceMin, "pmax": core.PriceMax, "owner": core.Owner,
		"withDeliveryOnly": core.WithDeliveryOnly, "withImagesOnly": core.WithImagesOnly, "byTitle": core.ByTitle,
	} {
		value, err := scalarText(raw)
		if err != nil {
			return SearchContext{}, fmt.Errorf("decode search filter %q: %w", key, err)
		}
		if value != "" {
			filters.Set(key, value)
		}
	}
	for key, requested := range options.Filters {
		actual := filters[key]
		// Avito represents an unselected top-level toggle as null.
		if len(actual) == 0 && len(requested) == 1 && requested[0] == "0" && !strings.HasPrefix(key, "params[") && key != "pmin" && key != "pmax" {
			actual = []string{"0"}
		}
		if !sameSelection(requested, actual) {
			return SearchContext{}, fmt.Errorf("server did not apply filter %q=%q (returned %q); rediscover with avito filters and retain search_args", key, requested, actual)
		}
	}
	sorting := core.Sort
	if sorting == "" || sorting == "default" {
		sorting = "101"
	}
	if options.Sort.String() != "" && options.Sort.String() != sorting {
		return SearchContext{}, fmt.Errorf("requested sort %q but Avito returned %q", options.Sort.String(), sorting)
	}
	result := SearchContext{Query: core.Query, LocationID: core.LocationID, CategoryID: core.CategoryID,
		LocalPriority: core.LocalPriority == 1, AppliedFilters: filters, Sort: sorting}
	result.SearchArgs = result.args()
	return result, nil
}

func sameSelection(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	normalize := func(values []string) {
		for i, value := range values {
			if decimalValue.MatchString(value) {
				// Decimal spelling can change in Avito's numeric JSON fields.
				value = strings.TrimLeft(value, "0")
				if value == "" || strings.HasPrefix(value, ".") {
					value = "0" + value
				}
				if strings.Contains(value, ".") {
					value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
				}
				values[i] = value
			}
		}
		sort.Strings(values)
	}
	normalize(a)
	normalize(b)
	return slices.Equal(a, b)
}

// Args contains flags only. Keep the original query as the positional argument
// so callers can change keywords without accidentally appending another query.
func (selection SearchContext) args() []string {
	args := []string{"--location-id", strconv.FormatInt(selection.LocationID, 10), "--local-priority=" + strconv.FormatBool(selection.LocalPriority)}
	if selection.CategoryID > 0 {
		args = append(args, "--category-id", strconv.FormatInt(selection.CategoryID, 10))
	}
	keys := make([]string, 0, len(selection.AppliedFilters))
	for key := range selection.AppliedFilters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range selection.AppliedFilters[key] {
			args = append(args, "--filter", key+"="+value)
		}
	}
	if selection.Sort != "" {
		args = append(args, "--sort", selection.Sort)
	}
	return args
}
