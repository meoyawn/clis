package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/meoyawn/clis/getmatch/internal/domain"
)

type searchPage struct {
	Meta *struct {
		Total  *int `json:"total"`
		Offset *int `json:"offset"`
		Limit  *int `json:"limit"`
	} `json:"meta"`
	Offers  []searchOffer `json:"offers"`
	Filters struct {
		Specializations []struct {
			Value string `json:"value"`
		} `json:"sp"`
	} `json:"filters"`
}

type searchOffer struct {
	ID              int                     `json:"id"`
	URL             string                  `json:"url"`
	Position        string                  `json:"position"`
	Active          *bool                   `json:"is_active"`
	Company         domain.SearchEmployer   `json:"company"`
	Published       string                  `json:"published_at"`
	Locations       []domain.SearchLocation `json:"location_items"`
	Skills          json.RawMessage         `json:"skills_objects"`
	SalaryFrom      *int                    `json:"salary_display_from"`
	SalaryTo        *int                    `json:"salary_display_to"`
	SalaryCurrency  *string                 `json:"salary_currency"`
	SalaryTaxes     *string                 `json:"salary_taxes"`
	SalaryHidden    *bool                   `json:"salary_hidden"`
	SalaryTotal     bool                    `json:"salary_is_total"`
	SalaryEstimated bool                    `json:"salary_by_our_version"`
}

// SearchVacancies reads one native page and matches skills only against its
// actual tags. Account settings, applications and private salary fields are not
// part of the result.
func (c *Client) SearchVacancies(ctx context.Context, options domain.VacancySearchOptions) (*domain.VacancySearchResult, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	offset := options.Page * options.PerPage
	query := url.Values{
		"p":      {strconv.Itoa(options.Page + 1)},
		"offset": {strconv.Itoa(offset)},
		"limit":  {strconv.Itoa(options.PerPage)},
		"sa":     {"any"},
		"pa":     {"all"},
	}
	if options.Salary != 0 {
		query.Set("sa", strconv.Itoa(options.Salary))
	}
	for _, slug := range options.Specializations {
		query.Add("sp", slug)
	}
	for _, level := range options.Levels {
		query.Add("se", level.String())
	}
	if options.Remote {
		query.Set("l", "remote")
	}
	var page searchPage
	if err := c.get(ctx, "/api/offers", query, &page); err != nil {
		return nil, err
	}
	if page.Meta == nil || page.Meta.Total == nil || page.Meta.Offset == nil || page.Meta.Limit == nil || page.Offers == nil {
		return nil, fmt.Errorf("search page %d is missing offers or pagination metadata; stopped", options.Page)
	}
	meta := page.Meta
	if *meta.Total < 0 || *meta.Offset != offset || *meta.Limit != options.PerPage || len(page.Offers) != min(options.PerPage, max(0, *meta.Total-offset)) {
		return nil, fmt.Errorf("search page %d has inconsistent total, offset, limit or offer count; stopped", options.Page)
	}
	for _, slug := range options.Specializations {
		found := false
		for _, specialization := range page.Filters.Specializations {
			if specialization.Value == slug {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("specialization %q is absent from the live vacancy catalog; stopped", slug)
		}
	}
	result := &domain.VacancySearchResult{
		URL: Origin + "/api/offers?" + query.Encode(), TotalCandidates: *meta.Total,
		Page: options.Page, ScannedCandidates: len(page.Offers),
		Vacancies: []domain.VacancySearchItem{},
	}
	if offset+len(page.Offers) < *meta.Total {
		next := options.Page + 1
		result.NextPage = &next
	}
	seen := make(map[string]bool, len(page.Offers))
	for _, offer := range page.Offers {
		item, active, err := searchItem(offer)
		if err != nil {
			return nil, err
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("search page %d repeats vacancy %s; stopped", options.Page, item.ID)
		}
		seen[item.ID] = true
		if options.Remote && !hasRemoteLocation(item.Locations) {
			return nil, fmt.Errorf("remote search returned vacancy %s without an included remote location; stopped", item.ID)
		}
		if active && (len(options.Skills) == 0 || matchesSearchSkills(item.KeySkills, options.Skills)) {
			result.Vacancies = append(result.Vacancies, item)
		}
	}
	return result, nil
}

func searchItem(offer searchOffer) (domain.VacancySearchItem, bool, error) {
	item := domain.VacancySearchItem{
		ID: strconv.Itoa(offer.ID), Title: offer.Position, Employer: offer.Company,
		Published: offer.Published, Locations: offer.Locations,
		URL: offer.URL,
	}
	if strings.HasPrefix(item.URL, "/vacancies/") {
		item.URL = Origin + item.URL
	}
	id, err := domain.NewVacancyIDFromValue(item.URL)
	if err != nil || !strings.HasPrefix(item.URL, Origin+"/vacancies/") || id.String() != item.ID || offer.Position == "" || offer.Company.Name == "" || offer.Active == nil || offer.SalaryHidden == nil || offer.Published == "" || offer.Locations == nil {
		return item, false, fmt.Errorf("search vacancy %d has missing or inconsistent identity, salary, publication or location fields; stopped", offer.ID)
	}
	if offer.Company.URL != "" && strings.HasPrefix(offer.Company.URL, "/companies/") {
		item.Employer.URL = Origin + offer.Company.URL
	}
	var skills []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(offer.Skills, &skills); err != nil {
		return item, false, fmt.Errorf("search vacancy %d has missing or invalid skills_objects; stopped", offer.ID)
	}
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" {
			return item, false, fmt.Errorf("search vacancy %d has an empty skill tag; stopped", offer.ID)
		}
		item.KeySkills = append(item.KeySkills, skill.Name)
	}
	item.Compensation = domain.VacancySearchCompensation{
		Hidden: *offer.SalaryHidden, Total: offer.SalaryTotal,
		Estimated: offer.SalaryEstimated, Taxes: offer.SalaryTaxes,
	}
	if !*offer.SalaryHidden {
		item.Compensation.From = offer.SalaryFrom
		item.Compensation.To = offer.SalaryTo
		item.Compensation.Currency = offer.SalaryCurrency
	}
	return item, *offer.Active, nil
}

func hasRemoteLocation(locations []domain.SearchLocation) bool {
	for _, location := range locations {
		if location.Format == "remote" && !location.Exclude {
			return true
		}
	}
	return false
}

func matchesSearchSkills(actual, requested []string) bool {
	for _, skill := range actual {
		for _, wanted := range requested {
			if strings.EqualFold(strings.TrimSpace(skill), strings.TrimSpace(wanted)) {
				return true
			}
		}
	}
	return false
}
