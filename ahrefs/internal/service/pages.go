package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/meoyawn/clis/ahrefs/internal/domain"
)

type PageOptions struct {
	Limit     int
	Offset    int
	All       bool
	SortBy    string
	Direction domain.Direction
}

func (o PageOptions) Validate() error {
	if o.Limit < 1 || o.Limit > 1000 {
		return fmt.Errorf("invalid limit %d; expected 1 through 1000 rows per request", o.Limit)
	}
	if o.Offset < 0 {
		return fmt.Errorf("invalid offset %d; expected a nonnegative row offset", o.Offset)
	}
	if _, err := domain.ParseColumns(o.SortBy); err != nil || strings.Contains(o.SortBy, ",") {
		return fmt.Errorf("invalid sort column %q; expected one Ahrefs field name", o.SortBy)
	}
	_, err := domain.NewDirectionFromValue(o.Direction.String())
	return err
}

func (c *Client) Pages(ctx context.Context, q domain.Query, options PageOptions) (*domain.PagesResult, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	s, err := c.selectCrawl(ctx, q)
	if err != nil {
		return nil, err
	}
	columns := slices.Clone(q.Columns)
	var filter any
	if q.FilterID != "" {
		filter = []string{"FilterId", q.FilterID}
	}
	if q.IssueID != "" {
		issue, err := c.issueByID(ctx, q.ProjectID, q.IssueID)
		if err != nil {
			return nil, err
		}
		if issue.Props.Pages == nil {
			return nil, fmt.Errorf("issue %q has no Pages report; link-only reports are not supported", q.IssueID)
		}
		descriptor := issue.Props.Pages
		if len(columns) == 0 {
			for _, field := range descriptor.Fields {
				name, err := fieldName(field)
				if err != nil {
					return nil, err
				}
				columns = append(columns, name)
			}
		}
		if filter == nil {
			q.FilterID = descriptor.FilterID
			if q.FilterID != "" {
				filter = []string{"FilterId", q.FilterID}
			} else if len(descriptor.Filter) > 0 && string(descriptor.Filter) != "null" {
				filter = []any{"FilterExpr", descriptor.Filter}
			} else {
				return nil, fmt.Errorf("issue %q has no page filter; refusing an unfiltered report", q.IssueID)
			}
		}
	}
	if len(columns) == 0 {
		columns = []string{"pageRating", "url", "traffic", "httpCode", "depth", "compliant"}
	}
	if filter == nil {
		// This is the unfiltered Page explorer, not an issue-specific view.
		filter = []any{"FilterExpr", map[string]any{
			"combinator": "and", "prev": false,
			"rules": []any{map[string]any{"field": []string{"Simple", "url"}, "operator": "exists", "prev": false}},
		}}
	}
	fields := make([][]string, 0, len(columns))
	for _, name := range columns {
		fields = append(fields, []string{"Simple", name})
	}
	input := s.input(q.ProjectID)
	input["input"] = map[string]any{
		"type": "Pages", "fields": fields, "filter": filter,
		"sort": []any{map[string]any{"by": []string{"Simple", options.SortBy}, "order": options.Direction.String()}},
	}
	input["de_mode"] = nil
	input["with_prev_value"] = false
	input["size"] = options.Limit
	result := &domain.PagesResult{
		ProjectID: q.ProjectID, Crawl: s.current, CompareWith: s.previous,
		IssueID: q.IssueID, FilterID: q.FilterID, Offset: options.Offset, Rows: []map[string]any{},
	}
	offset := options.Offset
	for {
		input["from"] = offset
		var response struct {
			Total *int `json:"total"`
			Table *struct {
				Headers [][]string `json:"headers"`
				Rows    []struct {
					ID     string            `json:"id"`
					Values []json.RawMessage `json:"values"`
				} `json:"rows"`
			} `json:"table"`
		}
		if err := c.call(ctx, "saDeTable", input, &response); err != nil {
			return nil, err
		}
		if response.Total == nil || *response.Total < 0 || response.Table == nil || len(response.Table.Headers) != len(columns) {
			return nil, fmt.Errorf("response from Ahrefs has an incomplete Pages table")
		}
		if offset != options.Offset && result.Total != *response.Total {
			return nil, fmt.Errorf("total Pages count changed during pagination; rerun with a fixed crawl")
		}
		result.Total = *response.Total
		names := make([]string, 0, len(columns))
		for _, header := range response.Table.Headers {
			name, err := fieldName(header)
			if err != nil {
				return nil, err
			}
			if slices.Contains(names, name) || !slices.Contains(columns, name) || name == "id" {
				return nil, fmt.Errorf("response from Ahrefs has an unexpected or duplicate page column %q", name)
			}
			names = append(names, name)
		}
		for _, row := range response.Table.Rows {
			if len(row.Values) != len(names) {
				return nil, fmt.Errorf("page %q has %d values for %d columns", row.ID, len(row.Values), len(names))
			}
			values := map[string]any{"id": row.ID}
			for i, raw := range row.Values {
				value, err := decodeCell(raw)
				if err != nil {
					return nil, fmt.Errorf("page %q column %q: %w", row.ID, names[i], err)
				}
				values[names[i]] = value
			}
			result.Rows = append(result.Rows, values)
		}
		offset += len(response.Table.Rows)
		result.NextOffset = nil
		if offset < result.Total {
			if len(response.Table.Rows) == 0 {
				return nil, fmt.Errorf("response from Ahrefs has an empty page at offset %d before total %d", offset, result.Total)
			}
			result.NextOffset = &offset
		}
		if !options.All || result.NextOffset == nil {
			return result, nil
		}
	}
}

func fieldName(field []string) (string, error) {
	if len(field) != 2 || field[0] != "Simple" {
		return "", fmt.Errorf("unsupported Ahrefs field %q; only Simple page fields are supported", field)
	}
	if _, err := domain.ParseColumns(field[1]); err != nil || strings.Contains(field[1], ",") {
		return "", fmt.Errorf("invalid Ahrefs field name %q", field[1])
	}
	return field[1], nil
}

func decodeCell(raw json.RawMessage) (any, error) {
	var tagged any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&tagged); err != nil {
		return nil, fmt.Errorf("decode table cell: %w", err)
	}
	return decodeTagged(tagged, 0)
}

func decodeTagged(value any, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("table cell nesting exceeded 32 levels")
	}
	pair, ok := value.([]any)
	if !ok || len(pair) != 2 {
		return nil, fmt.Errorf("expected a tagged table cell")
	}
	tag, ok := pair[0].(string)
	if !ok {
		return nil, fmt.Errorf("table cell tag is not a string")
	}
	switch tag {
	case "Option":
		if pair[1] == nil {
			return nil, nil
		}
		return decodeTagged(pair[1], depth+1)
	case "List":
		list, ok := pair[1].([]any)
		if !ok {
			return nil, fmt.Errorf("table cell with List tag is not an array")
		}
		values := make([]any, 0, len(list))
		for _, item := range list {
			decoded, err := decodeTagged(item, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, decoded)
		}
		return values, nil
	case "Keyword", "Url":
		if text, ok := pair[1].(string); ok {
			return text, nil
		}
	case "Long", "Double":
		if number, ok := pair[1].(json.Number); ok {
			return number, nil
		}
	case "Boolean":
		if boolean, ok := pair[1].(bool); ok {
			return boolean, nil
		}
	default:
		return nil, fmt.Errorf("unsupported table cell tag %q; extend the CLI decoder", tag)
	}
	return nil, fmt.Errorf("invalid value type for %s table cell", tag)
}
