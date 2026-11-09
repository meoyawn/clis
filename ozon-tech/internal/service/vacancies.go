package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/meoyawn/clis/ozon-tech/internal/domain"
	"golang.org/x/sync/errgroup"
)

const pageLimit = 20

func (c *Client) CheckAccess(ctx context.Context) error {
	var response struct {
		Items []domain.Vacancy `json:"items"`
		Total json.RawMessage  `json:"total"`
	}
	if err := c.request(ctx, apiPath+"list", map[string]any{"filters": map[string]any{}, "page": 1, "page_limit": 1}, &response); err != nil {
		return err
	}
	if _, err := parseTotal(response.Total); err != nil || response.Items == nil {
		return fmt.Errorf("invalid vacancy response during access check")
	}
	return nil
}

func (c *Client) Catalog(ctx context.Context, filters domain.Filters, details bool, concurrency int) (domain.Catalog, error) {
	if len(filters.Directions) == 0 || len(filters.WorkFormats) == 0 {
		return domain.Catalog{}, fmt.Errorf("directions and work formats must not be empty")
	}
	for _, value := range append(append([]string{}, filters.Directions...), filters.WorkFormats...) {
		if strings.TrimSpace(value) == "" {
			return domain.Catalog{}, fmt.Errorf("invalid empty filter value %q", value)
		}
	}
	if concurrency < 1 || concurrency > 16 {
		return domain.Catalog{}, fmt.Errorf("invalid concurrency %d; expected 1 to 16", concurrency)
	}
	catalog := domain.Catalog{SourceURL: filters.SourceURL(), Filters: filters, Vacancies: []domain.Vacancy{}}
	seenIDs, seenUUIDs := map[string]bool{}, map[string]bool{}
	for page := 1; ; page++ {
		var response struct {
			Items []domain.Vacancy `json:"items"`
			Total json.RawMessage  `json:"total"`
		}
		payload := struct {
			Filters   domain.Filters `json:"filters"`
			Page      int            `json:"page"`
			PageLimit int            `json:"page_limit"`
		}{filters, page, pageLimit}
		if err := c.request(ctx, apiPath+"list", payload, &response); err != nil {
			return domain.Catalog{}, fmt.Errorf("vacancy page %d: %w", page, err)
		}
		total, err := parseTotal(response.Total)
		if err != nil || response.Items == nil {
			return domain.Catalog{}, fmt.Errorf("invalid vacancy page %d: expected items and a nonnegative total", page)
		}
		if page == 1 {
			catalog.Count = total
		} else if total != catalog.Count {
			return domain.Catalog{}, fmt.Errorf("vacancy total changed from %d to %d on page %d; retry", catalog.Count, total, page)
		}
		expected := min(pageLimit, catalog.Count-len(catalog.Vacancies))
		if len(response.Items) != expected {
			return domain.Catalog{}, fmt.Errorf("incomplete vacancy page %d: got %d items, expected %d", page, len(response.Items), expected)
		}
		for _, item := range response.Items {
			if err := item.Validate(); err != nil {
				return domain.Catalog{}, err
			}
			if seenIDs[item.ID] || seenUUIDs[item.InternalUUID] {
				return domain.Catalog{}, fmt.Errorf("duplicate vacancy %s on page %d; retry", item.ID, page)
			}
			seenIDs[item.ID], seenUUIDs[item.InternalUUID] = true, true
			catalog.Vacancies = append(catalog.Vacancies, item)
		}
		catalog.PagesFetched++
		if len(catalog.Vacancies) == catalog.Count {
			break
		}
	}
	if details {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(concurrency)
		for i, item := range catalog.Vacancies {
			group.Go(func() error {
				full, err := c.Vacancy(groupCtx, item.ID)
				if err != nil {
					return err
				}
				if full.InternalUUID != item.InternalUUID || full.Slug != item.Slug {
					return fmt.Errorf("vacancy %s identity changed while fetching its description; retry", item.ID)
				}
				full.ExternalLink = item.ExternalLink
				catalog.Vacancies[i] = full
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return domain.Catalog{}, err
		}
	}
	catalog.SyncedAt = time.Now().UTC()
	return catalog, nil
}

func (c *Client) Vacancy(ctx context.Context, id string) (domain.Vacancy, error) {
	if err := domain.ValidateID(id); err != nil {
		return domain.Vacancy{}, err
	}
	var vacancy domain.Vacancy
	if err := c.request(ctx, apiPath+id, nil, &vacancy); err != nil {
		return domain.Vacancy{}, fmt.Errorf("vacancy %s: %w", id, err)
	}
	if err := vacancy.Validate(); err != nil {
		return domain.Vacancy{}, err
	}
	if vacancy.ID != id || vacancy.Description == nil {
		return domain.Vacancy{}, fmt.Errorf("vacancy %s returned a different ID or no description", id)
	}
	markdown, err := descriptionMarkdown(vacancy.Description)
	if err != nil {
		return domain.Vacancy{}, fmt.Errorf("vacancy %s: %w", id, err)
	}
	vacancy.DescriptionMarkdown = markdown
	checkedAt := time.Now().UTC()
	vacancy.DetailsCheckedAt = &checkedAt
	return vacancy, nil
}

func parseTotal(raw json.RawMessage) (int, error) {
	var value string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0, err
		}
	} else {
		value = string(raw)
	}
	total, err := strconv.Atoi(value)
	if err != nil || total < 0 || total > 100000 {
		return 0, fmt.Errorf("invalid vacancy total %q", value)
	}
	return total, nil
}
