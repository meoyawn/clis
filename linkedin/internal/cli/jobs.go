package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

// Captured from LinkedIn's jobs-search bundle and verified against two pages.
// The newer semantic-search HTML silently replaces Worldwide with a country.
const (
	jobCardsDecoration = "com.linkedin.voyager.dash.deco.jobs.search.JobSearchCardsCollection-221"
	jobsPageSize       = 25
	worldwideGeoURN    = "urn:li:fsd_geo:92000000"
)

type jobsOptions struct {
	limit int
	start int
	all   bool
}

type jobsResult struct {
	Company   companyRef   `json:"company"`
	Jobs      []domain.Job `json:"jobs"`
	Total     int          `json:"total"`
	Returned  int          `json:"returned"`
	Start     int          `json:"start"`
	HasMore   bool         `json:"has_more"`
	NextStart *int         `json:"next_start"`
	FetchedAt string       `json:"fetched_at"`
}

type jobCardsPage struct {
	Metadata struct {
		Geo struct {
			URN string `json:"entityUrn"`
		} `json:"geo"`
	} `json:"metadata"`
	Paging *struct {
		Start *int `json:"start"`
		Count int  `json:"count"`
		Total *int `json:"total"`
	} `json:"paging"`
	Elements []struct {
		Union struct {
			Card       *jobPostingCard `json:"jobPostingCard"`
			Suggestion json.RawMessage `json:"jobSearchSuggestion"`
		} `json:"jobCardUnion"`
	} `json:"elements"`
}

type jobPostingCard struct {
	URN      string   `json:"jobPostingUrn"`
	Title    string   `json:"jobPostingTitle"`
	Company  textView `json:"primaryDescription"`
	Location textView `json:"secondaryDescription"`
	Details  textView `json:"tertiaryDescription"`
	Logo     struct {
		Attributes []struct {
			Detail struct {
				CompanyURN string `json:"companyLogo"`
			} `json:"detailDataUnion"`
		} `json:"attributes"`
	} `json:"logo"`
	Footer []struct {
		Type   string `json:"type"`
		TimeAt int64  `json:"timeAt"`
	} `json:"footerItems"`
}

func runCompanyJobs(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("companies jobs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _ = writeHelp(stderr, []string{"companies", "jobs"}) }
	limit := fs.Int("limit", jobsPageSize, "maximum jobs to return")
	start := fs.Int("start", 0, "result offset")
	all := fs.Bool("all", false, "fetch all available job pages")
	positionals, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"companies", "jobs"})
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 {
		return fmt.Errorf("companies jobs requires one company slug, ID, or URL; got %d arguments", len(positionals))
	}
	company, err := parseCompany(positionals[0])
	if err != nil {
		return err
	}
	if *limit < 1 || *start < 0 {
		return fmt.Errorf("--limit must be positive and --start nonnegative; got limit=%d start=%d", *limit, *start)
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	result, err := client.jobs(ctx, company, jobsOptions{limit: *limit, start: *start, all: *all})
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func (c *linkedinClient) jobs(ctx context.Context, company companyRef, opts jobsOptions) (jobsResult, error) {
	company, err := c.resolve(ctx, company)
	if err != nil {
		return jobsResult{}, err
	}
	result := jobsResult{Company: company, Jobs: []domain.Job{}, Start: opts.start}
	seen := make(map[string]bool)
	for start := opts.start; ; {
		count := jobsPageSize
		if !opts.all {
			count = min(count, opts.limit-len(result.Jobs))
		}
		page, err := c.jobCards(ctx, company.ID, start, count)
		if err != nil {
			return jobsResult{}, fmt.Errorf("fetch jobs at offset %d: %w", start, err)
		}
		paging := page.Paging
		if paging == nil || paging.Start == nil || paging.Total == nil || page.Elements == nil {
			return jobsResult{}, fmt.Errorf("LinkedIn jobs response has changed (missing paging or elements)")
		}
		if *paging.Start != start || paging.Count <= 0 || paging.Count > count || *paging.Total < 0 || len(page.Elements) > paging.Count {
			return jobsResult{}, fmt.Errorf("LinkedIn returned unexpected jobs paging at offset %d", start)
		}
		if page.Metadata.Geo.URN != worldwideGeoURN {
			return jobsResult{}, fmt.Errorf("LinkedIn jobs search did not confirm Worldwide scope; got geo %q", page.Metadata.Geo.URN)
		}
		if (*paging.Total == 0 && len(result.Jobs) > 0) || (len(page.Elements) == 0 && start < *paging.Total) {
			return jobsResult{}, fmt.Errorf("LinkedIn stopped returning jobs at offset %d; search may be restricted", start)
		}
		result.Total = *paging.Total
		if (len(page.Elements) > 0 && start >= result.Total) || (len(page.Elements) < paging.Count && start+len(page.Elements) < result.Total) {
			return jobsResult{}, fmt.Errorf("LinkedIn returned incomplete jobs paging at offset %d", start)
		}
		previousCount := len(result.Jobs)
		for _, element := range page.Elements {
			card := element.Union.Card
			if card == nil {
				if len(element.Union.Suggestion) > 0 && string(element.Union.Suggestion) != "null" {
					continue
				}
				return jobsResult{}, fmt.Errorf("LinkedIn jobs response contains an unrecognized result at offset %d", start)
			}
			job, err := card.job(company.ID)
			if err != nil {
				return jobsResult{}, err
			}
			if !seen[job.ID] {
				seen[job.ID] = true
				result.Jobs = append(result.Jobs, job)
			}
		}
		next := start + paging.Count
		if next <= start {
			return jobsResult{}, fmt.Errorf("LinkedIn jobs pagination did not advance from offset %d", start)
		}
		if len(page.Elements) > 0 && len(result.Jobs) == previousCount {
			return jobsResult{}, fmt.Errorf("LinkedIn returned no new jobs at offset %d; stopped to avoid repeated pages", start)
		}
		result.HasMore = next < result.Total
		result.NextStart = nil
		if result.HasMore {
			result.NextStart = &next
		}
		if !result.HasMore || (!opts.all && len(result.Jobs) >= opts.limit) {
			break
		}
		start = next
	}
	result.Returned = len(result.Jobs)
	result.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

func (c *linkedinClient) jobCards(ctx context.Context, companyID string, start, count int) (jobCardsPage, error) {
	// Keep Rest.li punctuation literal, as in company/people requests. Request
	// expanded JSON explicitly: normalized+json would replace cards with URNs.
	path := fmt.Sprintf("/voyager/api/voyagerJobsDashJobCards?decorationId=%s&q=jobSearch&query=(origin:COMPANY_PAGE_JOBS_CLUSTER_EXPANSION,locationUnion:(geoId:92000000),selectedFilters:(company:List(%s)),spellCorrectionEnabled:true)&count=%d&start=%d", jobCardsDecoration, companyID, count, start)
	resp, err := c.get(ctx, path, http.Header{"Accept": {"application/json"}})
	if err != nil {
		return jobCardsPage{}, err
	}
	defer resp.Body.Close()
	const maxResponse = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return jobCardsPage{}, fmt.Errorf("read LinkedIn jobs: %w", err)
	}
	if len(body) > maxResponse {
		return jobCardsPage{}, fmt.Errorf("LinkedIn jobs response exceeds %d bytes", maxResponse)
	}
	var page jobCardsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return jobCardsPage{}, fmt.Errorf("LinkedIn did not return valid jobs JSON; check your session or capture the changed API response")
	}
	return page, nil
}

func (card jobPostingCard) job(companyID string) (domain.Job, error) {
	id, ok := strings.CutPrefix(card.URN, "urn:li:fsd_jobPosting:")
	if !ok || !digits.MatchString(id) || strings.TrimLeft(id, "0") == "" {
		return domain.Job{}, fmt.Errorf("LinkedIn returned invalid job URN %q", card.URN)
	}
	companyConfirmed := false
	for _, attribute := range card.Logo.Attributes {
		if attribute.Detail.CompanyURN == "urn:li:fsd_company:"+companyID {
			companyConfirmed = true
		}
	}
	if !companyConfirmed {
		return domain.Job{}, fmt.Errorf("LinkedIn job %s does not confirm company %s; no unrelated results returned", id, companyID)
	}
	job := domain.Job{
		ID: id, Title: strings.TrimSpace(card.Title), URL: "https://www.linkedin.com/jobs/view/" + id + "/",
		Company: strings.TrimSpace(card.Company.Text), Location: strings.TrimSpace(card.Location.Text),
		Details: strings.TrimSpace(card.Details.Text),
	}
	if job.Title == "" || job.Company == "" || job.Location == "" {
		return domain.Job{}, fmt.Errorf("LinkedIn job %s is missing its title, company, or location", id)
	}
	for _, footer := range card.Footer {
		if footer.Type == "LISTED_DATE" && footer.TimeAt > 0 {
			job.ListedAt = time.UnixMilli(footer.TimeAt).UTC().Format(time.RFC3339)
		}
	}
	return job, nil
}
