package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const filterSuggestPath = "/web/2/suggest/desktop"

type FilterOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Selected bool   `json:"selected"`
	Group    string `json:"group,omitempty"`
}

type FilterInput struct {
	Key      string   `json:"key"`
	Bound    string   `json:"bound"`
	Title    string   `json:"title,omitempty"`
	Selected string   `json:"selected,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
}

type Filter struct {
	Key            string         `json:"key"`
	Type           string         `json:"type"`
	Title          string         `json:"title"`
	Group          string         `json:"group,omitempty"`
	Flag           string         `json:"flag,omitempty"`
	Supported      bool           `json:"supported"`
	Multiple       bool           `json:"multiple"`
	UpdatesFilters bool           `json:"updates_filters"`
	HasSuggest     bool           `json:"has_suggest"`
	Selected       []string       `json:"selected"`
	Options        []FilterOption `json:"options"`
	Inputs         []FilterInput  `json:"inputs,omitempty"`
	Unit           string         `json:"unit,omitempty"`
}

type Category struct {
	Name       string     `json:"name"`
	CategoryID int64      `json:"category_id,omitempty"`
	Filters    url.Values `json:"filters"`
	Selected   bool       `json:"selected"`
	Count      *int64     `json:"count,omitempty"`
}

type FiltersResult struct {
	Metadata
	SearchContext
	Total        int64          `json:"total"`
	CategoryPath []Category     `json:"category_path"`
	Categories   []Category     `json:"categories"`
	Filters      []Filter       `json:"filters"`
	Sorting      []FilterOption `json:"sorting"`
}

type webFilterOption struct {
	Name    string            `json:"name"`
	Value   json.RawMessage   `json:"value"`
	Title   string            `json:"title"`
	Options []webFilterOption `json:"options"`
}

type webFilter struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Title       string            `json:"defaultTitle"`
	Current     json.RawMessage   `json:"currentValue"`
	Values      []webFilterOption `json:"values"`
	Selected    []webFilterOption `json:"selectedValue"`
	Content     []webFilter       `json:"content"`
	UpdatesForm bool              `json:"updatesForm"`
	HasSuggest  bool              `json:"hasSuggest"`
	Currency    string            `json:"currency"`
	Dimension   string            `json:"dimension"`
	Displaying  struct {
		Radio bool `json:"radio"`
	} `json:"displaying"`
	Inputs map[string]struct {
		ID      string          `json:"id"`
		Title   string          `json:"defaultTitle"`
		Current json.RawMessage `json:"currentValue"`
		Min     *float64        `json:"min"`
		Max     *float64        `json:"max"`
	} `json:"inputs"`
}

type webCategory struct {
	Name          string                     `json:"name"`
	CategoryID    int64                      `json:"categoryId"`
	Params        map[string]json.RawMessage `json:"params"`
	Current       bool                       `json:"current"`
	CurrentParent bool                       `json:"currentParent"`
	Count         *int64                     `json:"count"`
	Subs          []webCategory              `json:"subs"`
	CustomURL     string                     `json:"customUrl"`
}

func (c *Client) Filters(ctx context.Context, options SearchOptions, auth RequestOptions) (*FiltersResult, error) {
	if options.Page != 1 {
		return nil, fmt.Errorf("filters requires page 1, got %d", options.Page)
	}
	target, err := SearchURL(options)
	if err != nil {
		return nil, err
	}
	request, err := c.Prepare(ctx, target, auth, false)
	if err != nil {
		return nil, err
	}
	response, err := c.Do(request)
	if err != nil {
		return nil, err
	}
	result, err := ParseFilters(response.Body, options)
	if err != nil {
		return nil, err
	}
	stamp(&result.Metadata, request, response)
	if result.IsAuthenticated != nil && !*result.IsAuthenticated {
		result.Warnings = append(result.Warnings, "Avito returned a logged-out response despite saved cookies; refresh them with avito auth import.")
	}
	return result, nil
}

func ParseFilters(body []byte, options SearchOptions) (*FiltersResult, error) {
	payload, selection, err := readSearchPayload(body, options)
	if err != nil {
		return nil, err
	}
	if payload.Filters == nil || len(payload.Filters.Sections) == 0 {
		return nil, fmt.Errorf("web search response has no filtersV2.Sections; it may be a challenge or changed schema")
	}
	result := &FiltersResult{SearchContext: selection, Total: *payload.Count,
		Filters: []Filter{}, Sorting: []FilterOption{}, CategoryPath: []Category{}, Categories: []Category{}}
	result.IsAuthenticated = payload.Subscription.Authenticated
	if payload.SearchCore.Query != options.Query {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Server interpreted the query as %q.", payload.SearchCore.Query))
	}
	var visit func(webFilter, string) error
	visit = func(raw webFilter, group string) error {
		filter, err := parseFilter(raw)
		if err != nil {
			return err
		}
		filter.Group = group
		if filter.Key == "sort" {
			result.Sorting = filter.Options
		} else {
			result.Filters = append(result.Filters, filter)
		}
		for _, child := range raw.Content {
			if err := visit(child, filter.Title); err != nil {
				return err
			}
		}
		return nil
	}
	for _, section := range payload.Filters.Sections {
		for _, raw := range section.Filters {
			if err := visit(raw, ""); err != nil {
				return nil, err
			}
		}
	}
	if len(result.Filters) == 0 {
		return nil, fmt.Errorf("web search returned no supported filter form")
	}
	path, choices := categoryNavigation(payload.Rubricators.Categories)
	for _, raw := range path {
		category, err := parseCategory(raw)
		if err != nil {
			return nil, err
		}
		result.CategoryPath = append(result.CategoryPath, category)
	}
	for _, raw := range choices {
		if raw.CategoryID == 0 || raw.CustomURL != "" {
			continue
		}
		category, err := parseCategory(raw)
		if err != nil {
			return nil, err
		}
		result.Categories = append(result.Categories, category)
	}
	return result, nil
}

func parseFilter(raw webFilter) (Filter, error) {
	filter := Filter{Key: raw.ID, Type: raw.Type, Title: plainText(raw.Title),
		UpdatesFilters: raw.UpdatesForm, HasSuggest: raw.HasSuggest,
		Selected: []string{}, Options: []FilterOption{}, Unit: raw.Dimension}
	if filter.Key == "" || filter.Type == "" {
		return filter, fmt.Errorf("unsupported Avito filter schema for key %q", filter.Key)
	}
	if filter.Unit == "" {
		filter.Unit = raw.Currency
	}
	filter.Supported = supportedFilterKey(filter.Key)
	if filter.Supported {
		filter.Flag = "--filter"
	}
	switch raw.Type {
	case "checkboxGroup":
		if len(raw.Values) == 0 {
			filter.Supported, filter.Flag = false, ""
		}
	case "multiselect", "sectionedMultiselect":
		filter.Multiple = !raw.Displaying.Radio
	case "numericRange":
		filter.Supported = len(raw.Inputs) > 0
		for _, bound := range []string{"from", "to"} {
			input, exists := raw.Inputs[bound]
			if !exists {
				continue
			}
			value, err := scalarText(input.Current)
			if err != nil {
				return filter, fmt.Errorf("decode input %q: %w", input.ID, err)
			}
			filter.Inputs = append(filter.Inputs, FilterInput{Key: input.ID, Bound: bound, Title: input.Title, Selected: value, Min: input.Min, Max: input.Max})
			filter.Supported = filter.Supported && supportedFilterKey(input.ID)
		}
		if filter.Supported {
			filter.Flag = "--filter"
		}
	}
	if raw.Type != "numericRange" {
		selected, err := selectionValues(raw.Current)
		if err != nil {
			return filter, fmt.Errorf("decode current value for filter %q: %w", raw.ID, err)
		}
		filter.Selected = selected
	}
	if raw.ID == "localPriority" {
		filter.Supported, filter.Flag = true, "--local-priority"
	}
	if raw.ID == "sort" {
		filter.Supported, filter.Flag = true, "--sort"
		if len(filter.Selected) == 0 || filter.Selected[0] == "default" {
			filter.Selected = []string{"101"}
		}
	}
	seen := map[string]bool{}
	var visit func([]webFilterOption, string) error
	visit = func(options []webFilterOption, group string) error {
		for _, rawOption := range options {
			if rawOption.Options != nil {
				if err := visit(rawOption.Options, rawOption.Title); err != nil {
					return err
				}
				continue
			}
			value, err := scalarText(rawOption.Value)
			if err != nil || value == "" || rawOption.Name == "" {
				return fmt.Errorf("unsupported option in filter %q", raw.ID)
			}
			if seen[value] {
				continue
			}
			seen[value] = true
			filter.Options = append(filter.Options, FilterOption{Value: value, Label: plainText(rawOption.Name), Selected: slices.Contains(filter.Selected, value), Group: group})
		}
		return nil
	}
	if err := visit(raw.Values, ""); err != nil {
		return filter, err
	}
	if err := visit(raw.Selected, ""); err != nil {
		return filter, err
	}
	if raw.Type == "boolean" && len(filter.Options) == 0 {
		filter.Options = []FilterOption{{Value: "1", Label: filter.Title, Selected: slices.Contains(filter.Selected, "1")}}
	}
	if filter.Title == "" {
		if len(filter.Options) == 1 {
			filter.Title = filter.Options[0].Label
		} else {
			filter.Title = filter.Key
		}
	}
	return filter, nil
}

func categoryNavigation(nodes []webCategory) (path, choices []webCategory) {
	choices = nodes
	for _, node := range nodes {
		if node.Current {
			if len(node.Subs) > 0 {
				choices = node.Subs
			}
			return []webCategory{node}, choices
		}
		if node.CurrentParent {
			rest, descendants := categoryNavigation(node.Subs)
			return append([]webCategory{node}, rest...), descendants
		}
	}
	return nil, choices
}

func parseCategory(raw webCategory) (Category, error) {
	filters, err := flattenParams(raw.Params)
	if err != nil {
		return Category{}, err
	}
	return Category{Name: raw.Name, CategoryID: raw.CategoryID, Filters: filters, Selected: raw.Current, Count: raw.Count}, nil
}

type FilterValuesOptions struct {
	Search     SearchOptions
	Key        string
	ValueQuery string
	Offset     int
}

type FilterValuesResult struct {
	Metadata
	Query      string         `json:"query"`
	LocationID int64          `json:"location_id"`
	CategoryID int64          `json:"category_id"`
	Key        string         `json:"key"`
	ValueQuery string         `json:"value_query"`
	Count      int            `json:"count"`
	Total      int            `json:"total"`
	Offset     int            `json:"offset"`
	HasMore    bool           `json:"has_more"`
	NextOffset *int           `json:"next_offset,omitempty"`
	Options    []FilterOption `json:"options"`
}

func ValidateFilterValues(options FilterValuesOptions) error {
	if err := ValidateSearchOptions(options.Search); err != nil {
		return err
	}
	if options.Search.CategoryID <= 0 {
		return fmt.Errorf("--field needs --category-id and the current filters; copy search_args from avito filters")
	}
	matches := parameterKey.FindStringSubmatch(options.Key)
	if matches == nil || matches[2] != "" {
		return fmt.Errorf("field %q must be a parameter key such as params[123] with has_suggest: true", options.Key)
	}
	if options.Offset < 0 {
		return fmt.Errorf("offset %d must not be negative", options.Offset)
	}
	if len(options.ValueQuery) > 2048 || strings.ContainsAny(options.ValueQuery, "\x00\r\n") {
		return fmt.Errorf("value-query must be at most 2048 bytes without newlines or NUL")
	}
	return nil
}

func FilterValuesURL(options FilterValuesOptions) (*url.URL, error) {
	if err := ValidateFilterValues(options); err != nil {
		return nil, err
	}
	target, err := SearchURL(options.Search)
	if err != nil {
		return nil, err
	}
	target.Path = filterSuggestPath
	query := target.Query()
	query.Del("name") // query is the option text on this endpoint.
	query.Del("p")
	query.Set("paramId", parameterKey.FindStringSubmatch(options.Key)[1])
	query.Set("query", options.ValueQuery)
	query.Set("limit", "300")
	query.Set("offset", strconv.Itoa(options.Offset))
	target.RawQuery = query.Encode()
	return target, nil
}

func (c *Client) FilterValues(ctx context.Context, options FilterValuesOptions, auth RequestOptions) (*FilterValuesResult, error) {
	target, err := FilterValuesURL(options)
	if err != nil {
		return nil, err
	}
	request, err := c.Prepare(ctx, target, auth, false)
	if err != nil {
		return nil, err
	}
	response, err := c.Do(request)
	if err != nil {
		return nil, err
	}
	result, err := ParseFilterValues(response.Body, options)
	if err != nil {
		return nil, err
	}
	stamp(&result.Metadata, request, response)
	return result, nil
}

func ParseFilterValues(body []byte, options FilterValuesOptions) (*FilterValuesResult, error) {
	type value struct {
		ID    json.RawMessage `json:"id"`
		Title string          `json:"title"`
	}
	var payload struct {
		Status string `json:"status"`
		Result *struct {
			Total    *int    `json:"optionsTotal"`
			Next     *int    `json:"nextOptionsOffset"`
			Selected []value `json:"selectedValue"`
			Sections []struct {
				Label   string  `json:"label"`
				Options []value `json:"options"`
			} `json:"sections"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode filter values: %w", err)
	}
	if payload.Status != "ok" || payload.Result == nil || payload.Result.Total == nil || *payload.Result.Total < 0 {
		return nil, fmt.Errorf("web response has no valid filter values result")
	}
	data := payload.Result
	result := &FilterValuesResult{Query: options.Search.Query, LocationID: options.Search.LocationID,
		CategoryID: options.Search.CategoryID, Key: options.Key, ValueQuery: options.ValueQuery,
		Offset: options.Offset, Total: *data.Total, Options: []FilterOption{}}
	selected := map[string]bool{}
	for _, option := range data.Selected {
		id, err := scalarText(option.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid selected filter value for %q", options.Key)
		}
		selected[id] = true
	}
	seen := map[string]bool{}
	for _, section := range data.Sections {
		for _, option := range section.Options {
			id, err := scalarText(option.ID)
			if err != nil || id == "" || option.Title == "" {
				return nil, fmt.Errorf("invalid filter option for %q", options.Key)
			}
			if !seen[id] {
				result.Options = append(result.Options, FilterOption{Value: id, Label: option.Title, Selected: selected[id], Group: section.Label})
				seen[id] = true
			}
		}
	}
	result.Count = len(result.Options)
	if result.Count == 0 && result.Total > options.Offset {
		return nil, fmt.Errorf("web response has no options for %q despite total %d at offset %d", options.Key, result.Total, options.Offset)
	}
	if data.Next != nil && *data.Next > options.Offset && *data.Next < result.Total {
		result.HasMore, result.NextOffset = true, data.Next
	}
	return result, nil
}
