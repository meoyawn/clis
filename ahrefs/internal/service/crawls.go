package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/meoyawn/clis/ahrefs/internal/domain"
)

type wireCrawl struct {
	ID       int             `json:"crawlId"`
	Status   json.RawMessage `json:"status"`
	Start    string          `json:"start"`
	Finished *string         `json:"finished"`
}

func (w wireCrawl) crawl() (domain.Crawl, error) {
	var status []json.RawMessage
	var name string
	if err := json.Unmarshal(w.Status, &status); err != nil || len(status) == 0 || json.Unmarshal(status[0], &name) != nil {
		return domain.Crawl{}, fmt.Errorf("crawl %d has an invalid status", w.ID)
	}
	c := domain.Crawl{ID: w.ID, Status: name, StartedAt: w.Start}
	if w.Finished != nil {
		var err error
		c.Timestamp, err = domain.NormalizeTimestamp(*w.Finished)
		if err != nil {
			return domain.Crawl{}, err
		}
	}
	return c, nil
}

func (c *Client) Projects(ctx context.Context) ([]domain.Project, error) {
	var response struct {
		Projects []struct {
			ID        string     `json:"id"`
			Name      string     `json:"name"`
			Target    string     `json:"target"`
			LastCrawl *wireCrawl `json:"lastCrawl"`
		} `json:"projects"`
	}
	if err := c.call(ctx, "saProjects", nil, &response); err != nil {
		return nil, err
	}
	if response.Projects == nil {
		return nil, fmt.Errorf("response from Ahrefs has no projects array")
	}
	projects := make([]domain.Project, 0, len(response.Projects))
	for _, p := range response.Projects {
		project := domain.Project{ID: p.ID, Name: p.Name, Target: p.Target}
		if p.LastCrawl != nil {
			crawl, err := p.LastCrawl.crawl()
			if err != nil {
				return nil, err
			}
			project.LastCrawl = &crawl
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func (c *Client) Crawls(ctx context.Context, projectID string) ([]domain.Crawl, error) {
	if err := (domain.Query{ProjectID: projectID}).Validate(); err != nil {
		return nil, err
	}
	var response struct {
		Crawls []wireCrawl `json:"crawls"`
	}
	if err := c.call(ctx, "saCrawls", map[string]any{"project_id": projectID}, &response); err != nil {
		return nil, err
	}
	if response.Crawls == nil {
		return nil, fmt.Errorf("response from Ahrefs has no crawls array for project %s", projectID)
	}
	crawls := make([]domain.Crawl, 0, len(response.Crawls))
	for _, w := range response.Crawls {
		crawl, err := w.crawl()
		if err != nil {
			return nil, err
		}
		crawls = append(crawls, crawl)
	}
	slices.SortStableFunc(crawls, func(a, b domain.Crawl) int { return strings.Compare(b.StartedAt, a.StartedAt) })
	return crawls, nil
}

type selection struct {
	current, previous, beforePrevious string
}

func (s selection) timestamp() map[string]any {
	var previous any = "None"
	if s.previous != "" {
		previous = []string{"Some", s.previous}
	}
	return map[string]any{"timestamp": []string{"Some", s.current}, "compare_with": previous}
}

func (s selection) input(projectID string) map[string]any {
	return map[string]any{"project_id": projectID, "timestamp": s.timestamp(), "global_filter_id": nil}
}

func (c *Client) selectCrawl(ctx context.Context, q domain.Query) (selection, error) {
	if err := q.Validate(); err != nil {
		return selection{}, err
	}
	crawls, err := c.Crawls(ctx, q.ProjectID)
	if err != nil {
		return selection{}, err
	}
	var timestamps []string
	for _, crawl := range crawls {
		if crawl.Status == "Completed" && crawl.Timestamp != "" {
			timestamps = append(timestamps, crawl.Timestamp)
		}
	}
	slices.SortFunc(timestamps, func(a, b string) int { return strings.Compare(b, a) })
	if len(timestamps) == 0 {
		return selection{}, fmt.Errorf("project %s has no completed crawls", q.ProjectID)
	}
	current, err := domain.NormalizeTimestamp(q.Crawl)
	if err != nil {
		return selection{}, err
	}
	if current == "" {
		current = timestamps[0]
	}
	index := slices.Index(timestamps, current)
	if index < 0 {
		return selection{}, fmt.Errorf("completed crawl %q not found in project %s; use crawls to choose a timestamp", current, q.ProjectID)
	}
	s := selection{current: current}
	if index+1 < len(timestamps) {
		s.previous = timestamps[index+1]
	}
	if q.CompareWith != nil {
		s.previous, err = domain.NormalizeTimestamp(*q.CompareWith)
		if err != nil {
			return selection{}, err
		}
	}
	if s.previous != "" {
		index = slices.Index(timestamps, s.previous)
		if index < 0 || s.previous >= s.current {
			return selection{}, fmt.Errorf("comparison crawl %q must be a completed crawl before %q in project %s", s.previous, s.current, q.ProjectID)
		}
		if index+1 < len(timestamps) {
			s.beforePrevious = timestamps[index+1]
		}
	}
	return s, nil
}
