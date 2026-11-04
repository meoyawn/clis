package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/meoyawn/clis/ahrefs/internal/domain"
)

type wireDescription struct {
	Fields   [][]string      `json:"fields"`
	FilterID string          `json:"filter_id"`
	Filter   json.RawMessage `json:"filter"`
}

type wireIssue struct {
	ID     string `json:"issue_id"`
	Active *bool  `json:"is_active"`
	Props  struct {
		Name        string           `json:"name"`
		Level       string           `json:"level"`
		Category    string           `json:"typ"`
		Pages       *wireDescription `json:"pages_descr"`
		Description []string         `json:"description"`
		HowToFix    []string         `json:"how_to_fix"`
	} `json:"props"`
}

func (w wireIssue) issue(details bool) (domain.Issue, error) {
	issue := domain.Issue{ID: w.ID, Name: w.Props.Name, Level: w.Props.Level, Category: w.Props.Category, Active: w.Active}
	if !details {
		return issue, nil
	}
	if w.Props.Pages != nil {
		issue.FilterID = w.Props.Pages.FilterID
		for _, field := range w.Props.Pages.Fields {
			name, err := fieldName(field)
			if err != nil {
				return domain.Issue{}, fmt.Errorf("issue %s: %w", w.ID, err)
			}
			issue.Columns = append(issue.Columns, name)
		}
	}
	for _, description := range []struct {
		wire []string
		out  *string
	}{{w.Props.Description, &issue.Description}, {w.Props.HowToFix, &issue.HowToFix}} {
		if len(description.wire) == 2 && description.wire[0] == "Markdown" {
			*description.out = description.wire[1]
		} else if len(description.wire) > 0 {
			return domain.Issue{}, fmt.Errorf("issue %s has an unsupported description format", w.ID)
		}
	}
	return issue, nil
}

func (c *Client) issueByID(ctx context.Context, projectID, issueID string) (wireIssue, error) {
	if err := (domain.Query{ProjectID: projectID, IssueID: issueID}).Validate(); err != nil {
		return wireIssue{}, err
	}
	if issueID == "" {
		return wireIssue{}, fmt.Errorf("issue ID %q is empty; choose an ID from issues", issueID)
	}
	var response struct {
		Issue wireIssue `json:"issue"`
	}
	if err := c.call(ctx, "saGetProjectIssueById", map[string]any{"project_id": projectID, "issue_id": issueID}, &response); err != nil {
		return wireIssue{}, err
	}
	if response.Issue.ID != issueID {
		return wireIssue{}, fmt.Errorf("response from Ahrefs has no matching issue for %q in project %s", issueID, projectID)
	}
	return response.Issue, nil
}

func (c *Client) Issue(ctx context.Context, projectID, issueID string) (domain.Issue, error) {
	w, err := c.issueByID(ctx, projectID, issueID)
	if err != nil {
		return domain.Issue{}, err
	}
	return w.issue(true)
}

type IssueOptions struct {
	All    bool
	Level  *domain.Level
	Search string
}

func (c *Client) Issues(ctx context.Context, q domain.Query, options IssueOptions) (*domain.IssuesResult, error) {
	if q.IssueID != "" || q.FilterID != "" || len(q.Columns) > 0 {
		return nil, fmt.Errorf("issues requires a project ID or All issues URL; use pages for a filtered Data Explorer URL")
	}
	if options.Level != nil {
		if _, err := domain.NewLevelFromValue(options.Level.String()); err != nil {
			return nil, err
		}
	}
	s, err := c.selectCrawl(ctx, q)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Issue wireIssue `json:"issue"`
		Count *int      `json:"count"`
	}
	if err := c.call(ctx, "saGetProjectIssues", s.input(q.ProjectID), &entries); err != nil {
		return nil, err
	}
	changes := map[string]domain.Changes{}
	if s.previous != "" {
		input := s.input(q.ProjectID)
		input["timestamp2"] = (selection{current: s.previous, previous: s.beforePrevious}).timestamp()
		var diffs []struct {
			ID string `json:"id"`
			domain.Changes
			New *int `json:"new_"`
		}
		if err := c.call(ctx, "saGetDiffsByIssues", input, &diffs); err != nil {
			return nil, err
		}
		for _, diff := range diffs {
			diff.Changes.New = diff.New
			changes[diff.ID] = diff.Changes
		}
	}
	result := &domain.IssuesResult{ProjectID: q.ProjectID, Crawl: s.current, CompareWith: s.previous, Issues: []domain.Issue{}}
	for _, entry := range entries {
		w := entry.Issue
		if !options.All && ((w.Active != nil && !*w.Active) || entry.Count == nil || *entry.Count == 0) {
			continue
		}
		if options.Level != nil && w.Props.Level != options.Level.String() {
			continue
		}
		if !strings.Contains(strings.ToLower(w.Props.Name), strings.ToLower(options.Search)) {
			continue
		}
		issue, err := w.issue(false)
		if err != nil {
			return nil, err
		}
		issue.Count = entry.Count
		if diff, ok := changes[issue.ID]; ok {
			issue.Changes = &diff
		}
		result.Issues = append(result.Issues, issue)
	}
	return result, nil
}
