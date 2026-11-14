package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

var keywordDomainPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,}$`)
var keywordCountryPattern = regexp.MustCompile(`^[a-z]{2}$`)

type KeywordOptions struct {
	Domain  string
	Country string
	Limit   int
	Offset  int
}

func (o KeywordOptions) Validate() error {
	if !keywordDomainPattern.MatchString(o.Domain) {
		return fmt.Errorf("invalid domain %q; expected a bare hostname", o.Domain)
	}
	if !keywordCountryPattern.MatchString(o.Country) {
		return fmt.Errorf("invalid country %q; expected a lowercase two-letter database code", o.Country)
	}
	if o.Limit < 1 || o.Limit > 1000 || o.Offset < 0 {
		return fmt.Errorf("invalid limit %d or offset %d; limit must be 1–1000 and offset nonnegative", o.Limit, o.Offset)
	}
	return nil
}

type KeywordResult struct {
	Count      int                          `json:"count"`
	Offset     int                          `json:"offset"`
	NextOffset *int                         `json:"next_offset,omitempty"`
	Rows       []map[string]json.RawMessage `json:"rows"`
}

func (c *Client) OrganicKeywords(ctx context.Context, o KeywordOptions) (*KeywordResult, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	fields := []string{"text", "country", "volume", "volume_avg", "difficulty", "cpc", "traffic_ml", "best_position", "best_position_url", "best_position_kind", "last_update"}
	field := func(name string) []any {
		return []any{"Direct", map[string]any{"field": []string{"Keywords", "Keyword", name}, "modifier": nil}}
	}
	shape := make([]any, 0, len(fields))
	for _, name := range fields {
		shape = append(shape, map[string]any{"field_name": field(name), "alias": nil})
	}
	target := map[string]any{"target": o.Domain + "/", "mode": "subdomains", "protocol": "both"}
	input := map[string]any{
		"args":   map[string]any{"mode": "subdomains", "protocol": "both", "url": o.Domain + "/", "multiTarget": []any{"Single", target}, "reportMode": []any{"Actual", "Today"}, "country": o.Country, "groupBy": "location"},
		"params": map[string]any{"size": o.Limit, "offset": o.Offset, "order_by": []any{[]any{"Desc", field("traffic_ml")}}, "shape": shape},
	}
	var raw struct {
		Count int                 `json:"count"`
		Rows  [][]json.RawMessage `json:"rows"`
	}
	if err := c.get(ctx, "seGetOrganicKeywords", input, &raw); err != nil {
		return nil, err
	}
	result := &KeywordResult{Count: raw.Count, Offset: o.Offset, Rows: make([]map[string]json.RawMessage, 0, len(raw.Rows))}
	for _, row := range raw.Rows {
		if len(row) != len(fields) {
			return nil, fmt.Errorf("keyword row from Ahrefs has %d cells; expected %d", len(row), len(fields))
		}
		values := make(map[string]json.RawMessage, len(fields))
		for i, name := range fields {
			values[name] = row[i]
		}
		result.Rows = append(result.Rows, values)
	}
	if next := o.Offset + len(raw.Rows); next < raw.Count {
		if len(raw.Rows) == 0 {
			return nil, fmt.Errorf("keyword pagination from Ahrefs returned an empty page before count %d", raw.Count)
		}
		result.NextOffset = &next
	}
	return result, nil
}

func (c *Client) RestrictedAccess(ctx context.Context) (map[string]bool, error) {
	var result map[string]bool
	if err := c.call(ctx, "tkGetRestrictedToolsAccess", nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}
