package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const browserTimestamp = "02-01-2006T150405"

var (
	projectPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	fieldPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type Query struct {
	ProjectID   string
	Crawl       string
	CompareWith *string // nil selects the preceding crawl; empty disables comparison.
	IssueID     string
	FilterID    string
	Columns     []string
}

func ParseTarget(input string) (Query, error) {
	if projectPattern.MatchString(input) {
		return Query{ProjectID: input}, nil
	}
	u, err := url.Parse(input)
	if err != nil || u.Scheme != "https" || u.Host != "app.ahrefs.com" || u.User != nil || u.Fragment != "" {
		return Query{}, fmt.Errorf("invalid target URL; use a positive project ID or an https://app.ahrefs.com/site-audit/ URL without embedded credentials or fragments")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "site-audit" || !projectPattern.MatchString(parts[1]) {
		return Query{}, fmt.Errorf("invalid Site Audit path %q; expected /site-audit/PROJECT[/issues|data-explorer|overview]", u.Path)
	}
	if len(parts) == 3 && !slices.Contains([]string{"issues", "data-explorer", "overview"}, parts[2]) {
		return Query{}, fmt.Errorf("unsupported Site Audit page %q; use issues, data-explorer, or overview", parts[2])
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Query{}, fmt.Errorf("invalid Site Audit query: %w", err)
	}
	for key, vals := range values {
		switch key {
		case "current", "columns", "issueId", "filterId", "filterCollapsed":
		default:
			return Query{}, fmt.Errorf("unsupported URL parameter %q; extend the CLI before using this view", key)
		}
		if len(vals) != 1 {
			return Query{}, fmt.Errorf("URL parameter %q must occur exactly once", key)
		}
	}
	q := Query{ProjectID: parts[1], IssueID: values.Get("issueId"), FilterID: values.Get("filterId")}
	q.Crawl, err = NormalizeTimestamp(values.Get("current"))
	if err != nil {
		return Query{}, err
	}
	if values.Has("columns") {
		q.Columns, err = ParseColumns(values.Get("columns"))
		if err != nil {
			return Query{}, err
		}
	}
	return q, q.Validate()
}

func NormalizeTimestamp(value string) (string, error) {
	if value == "" || value == "latest" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339, browserTimestamp} {
		t, err := time.Parse(layout, value)
		if err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("invalid crawl timestamp %q; use latest, RFC3339, or DD-MM-YYYYTHHMMSS", value)
}

func ParseColumns(value string) ([]string, error) {
	fields := strings.Split(value, ",")
	for i, field := range fields {
		if !fieldPattern.MatchString(field) || slices.Contains(fields[:i], field) {
			return nil, fmt.Errorf("invalid or duplicate column %q in %q; use unique Ahrefs field names separated by commas", field, value)
		}
	}
	return fields, nil
}

func (q Query) Validate() error {
	if !projectPattern.MatchString(q.ProjectID) {
		return fmt.Errorf("invalid project ID %q; expected a positive numeric ID", q.ProjectID)
	}
	for name, value := range map[string]string{"issue ID": q.IssueID, "filter ID": q.FilterID} {
		if value != "" && !idPattern.MatchString(value) {
			return fmt.Errorf("invalid %s %q; expected letters, digits, hyphens, or underscores", name, value)
		}
	}
	if _, err := NormalizeTimestamp(q.Crawl); err != nil {
		return err
	}
	if q.CompareWith != nil && *q.CompareWith != "" {
		timestamp, err := NormalizeTimestamp(*q.CompareWith)
		if err != nil {
			return err
		}
		if timestamp == "" {
			return fmt.Errorf("invalid comparison %q; choose a timestamp or none", *q.CompareWith)
		}
	}
	if len(q.Columns) > 0 {
		_, err := ParseColumns(strings.Join(q.Columns, ","))
		return err
	}
	return nil
}
