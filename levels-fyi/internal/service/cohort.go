package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/meoyawn/clis/levels-fyi/internal/domain"
)

// Cohort queries each explicitly selected employer through the native company
// filter. Exact focus tags are applied to returned rows, not broad searchText.
func (c *Client) Cohort(ctx context.Context, q domain.Query, companies, focusTags []string, since time.Time) (*domain.CohortReport, error) {
	if len(companies) == 0 || len(companies) > 20 || q.Company != "" {
		return nil, fmt.Errorf("cohort requires 1..20 company slugs and no single company filter; got %d companies and company=%q", len(companies), q.Company)
	}
	seenCompanies := map[string]bool{}
	for _, company := range companies {
		if company == "" || strings.ContainsAny(company, "/?# ,") || seenCompanies[company] {
			return nil, fmt.Errorf("invalid or duplicate company slug %q", company)
		}
		seenCompanies[company] = true
	}
	for _, focus := range focusTags {
		if strings.TrimSpace(focus) == "" {
			return nil, fmt.Errorf("exact focus tag must not be empty: %q", focus)
		}
	}
	r := &domain.CohortReport{FetchedAt: time.Now().UTC(), Companies: companies, FocusTags: focusTags, Query: q, LevelFilter: q.Level.String(), Employers: []domain.EmployerSample{}, Submissions: []domain.Submission{}, Evidence: []string{}}
	ids := map[string]bool{}
	for i, company := range companies {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
		cq := q
		cq.Company = company
		sample, err := c.Estimate(ctx, cq, since)
		if err != nil {
			return nil, fmt.Errorf("company %q: %w; no partial cohort returned", company, err)
		}
		selected := []domain.Submission{}
		r.RetrievedCount += sample.RetrievedCount
		for _, row := range sample.Submissions {
			if ids[row.ID] || len(focusTags) > 0 && (row.Focus == nil || !slices.Contains(focusTags, *row.Focus)) {
				continue
			}
			ids[row.ID] = true
			selected = append(selected, row)
			r.Submissions = append(r.Submissions, row)
		}
		r.Employers = append(r.Employers, domain.EmployerSample{Company: company, SourceReportedTotal: sample.SourceReportedTotal, RetrievedCount: sample.RetrievedCount, SelectedCount: len(selected), Groups: groups(selected, false)})
		r.Evidence = append(r.Evidence, sample.Evidence...)
	}
	r.SelectedCount = len(r.Submissions)
	r.ExcludedCount = r.RetrievedCount - r.SelectedCount
	r.Groups = groups(r.Submissions, false)
	r.Countries = groups(r.Submissions, true)
	return r, nil
}
