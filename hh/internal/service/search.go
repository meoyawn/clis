package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/meoyawn/clis/hh/internal/domain"
)

// SearchVacancies reads one native candidate page. Skill matching uses only the
// live vacancy's keySkills; text mentions and similarly named tags do not qualify.
func (c *Client) SearchVacancies(ctx context.Context, options domain.VacancySearchOptions) (*domain.VacancySearchResult, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	query := url.Values{
		"text":          {options.QueryText()},
		"page":          {strconv.Itoa(options.Page)},
		"items_on_page": {strconv.Itoa(options.PerPage)},
	}
	for _, area := range options.Areas {
		query.Add("area", area)
	}
	if options.Remote {
		query.Set("work_format", "REMOTE")
	}
	body, err := c.request(ctx, http.MethodGet, "/search/vacancy", query, nil, true)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(body)
	if err != nil {
		return nil, err
	}
	list, _ := state["vacancySearchResult"].(map[string]any)
	criteria, _ := list["criteria"].(map[string]any)
	if criteria["text"] != options.QueryText() || nativeID(criteria["page"]) != strconv.Itoa(options.Page) || nativeID(criteria["items_on_page"]) != strconv.Itoa(options.PerPage) {
		return nil, fmt.Errorf("HH search criteria do not match requested text, page %d or per-page %d; stopped", options.Page, options.PerPage)
	}
	if !searchSameValues(criteria["area"], options.Areas) {
		return nil, fmt.Errorf("HH search area criteria %v do not match requested areas %v; stopped", criteria["area"], options.Areas)
	}
	var formats []string
	if options.Remote {
		formats = []string{"REMOTE"}
	}
	if !searchSameValues(criteria["work_format"], formats) {
		return nil, fmt.Errorf("HH search work_format criteria %v do not match requested formats %v; stopped", criteria["work_format"], formats)
	}
	total, err := strconv.Atoi(nativeID(list["totalResults"]))
	if err != nil || total < 0 {
		return nil, fmt.Errorf("invalid HH search totalResults %v", list["totalResults"])
	}
	rows, ok := list["vacancies"].([]any)
	if !ok || len(rows) > options.PerPage || len(rows) > total {
		return nil, fmt.Errorf("HH search page %d has an invalid vacancy list", options.Page)
	}
	result := &domain.VacancySearchResult{
		URL:             c.base.ResolveReference(&url.URL{Path: "/search/vacancy", RawQuery: query.Encode()}).String(),
		TotalCandidates: total, Page: options.Page, ScannedCandidates: len(rows),
		Vacancies: []domain.VacancySearchItem{},
	}
	paging, _ := list["paging"].(map[string]any)
	next, _ := paging["next"].(map[string]any)
	if next != nil {
		disabled, ok := next["disabled"].(bool)
		if !ok {
			return nil, fmt.Errorf("HH search page %d has invalid next-page metadata", options.Page)
		}
		if !disabled {
			page, err := strconv.Atoi(nativeID(next["page"]))
			if err != nil || page != options.Page+1 || len(rows) == 0 {
				return nil, fmt.Errorf("HH search page %d has invalid next page %v", options.Page, next["page"])
			}
			if page <= 1999/options.PerPage {
				result.NextPage = &page
			}
		}
	} else if min(total, 2000) > (options.Page+1)*options.PerPage {
		return nil, fmt.Errorf("HH search page %d is missing next-page metadata; stopped", options.Page)
	}
	seen := make(map[string]bool, len(rows))
	for _, value := range rows {
		row, _ := value.(map[string]any)
		item, err := c.searchItem(row)
		if err != nil {
			return nil, err
		}
		if seen[item.ID] {
			return nil, fmt.Errorf("HH search page %d repeats vacancy %s", options.Page, item.ID)
		}
		seen[item.ID] = true
		if options.Remote && !slices.Contains(item.WorkFormats, "REMOTE") {
			return nil, fmt.Errorf("HH remote search returned vacancy %s without REMOTE work format", item.ID)
		}
		if len(options.Skills) > 0 {
			matched, err := c.searchSkillItem(ctx, &item, options)
			if err != nil {
				return nil, err
			}
			if !matched {
				continue
			}
		}
		result.Vacancies = append(result.Vacancies, item)
	}
	return result, nil
}

func (c *Client) searchSkillItem(ctx context.Context, item *domain.VacancySearchItem, options domain.VacancySearchOptions) (bool, error) {
	id, err := NewVacancyIDFromValue(item.ID)
	if err != nil {
		return false, err
	}
	page, err := c.readVacancyPage(ctx, id)
	if err != nil {
		return false, fmt.Errorf("read key skills for vacancy %s: %w", id, err)
	}
	data := page.data
	employer, err := searchEmployer(data)
	if err != nil || employer.ID != item.Employer.ID || employer.Name != item.Employer.Name {
		return false, fmt.Errorf("search vacancy %s employer changed or is unverifiable during key-skill verification", id)
	}
	item.KeySkills, err = searchStrings(data["keySkills"])
	if err != nil {
		return false, fmt.Errorf("vacancy %s has invalid or missing keySkills: %w", id, err)
	}
	item.WorkFormats, err = searchStrings(data["workFormats"])
	if err != nil {
		return false, fmt.Errorf("vacancy %s has invalid workFormats: %w", id, err)
	}
	status, _ := data["status"].(map[string]any)
	if status["archived"] == true || data["closedForApplicants"] == true || options.Remote && !slices.Contains(item.WorkFormats, "REMOTE") || !matchesSearchSkills(item.KeySkills, options.Skills) {
		return false, nil
	}
	item.Title, _ = data["name"].(string)
	if item.Title == "" {
		return false, fmt.Errorf("vacancy %s has no live title", id)
	}
	item.Published, _ = data["publicationTimeIso"].(string)
	compensation, _ := data["compensation"].(map[string]any)
	item.Compensation = searchCompensation(compensation)
	return true, nil
}

func (c *Client) searchItem(row Object) (domain.VacancySearchItem, error) {
	item := domain.VacancySearchItem{ID: nativeID(row["vacancyId"])}
	if _, err := NewVacancyIDFromValue(item.ID); err != nil {
		return item, err
	}
	var err error
	item.Employer, err = searchEmployer(row)
	if err != nil {
		return item, fmt.Errorf("HH search vacancy %s: %w", item.ID, err)
	}
	area, _ := row["area"].(map[string]any)
	item.Title, _ = row["name"].(string)
	item.Area.ID = nativeID(area["@id"])
	item.Area.Name, _ = area["name"].(string)
	if item.Title == "" || item.Area.ID == "" || item.Area.Name == "" {
		return item, fmt.Errorf("HH search vacancy %s is missing title or area identity", item.ID)
	}
	item.URL = c.base.String() + "/vacancy/" + item.ID
	compensation, _ := row["compensation"].(map[string]any)
	item.Compensation = searchCompensation(compensation)
	published, _ := row["publicationTime"].(map[string]any)
	item.Published, _ = published["$"].(string)
	formats, _ := row["workFormats"].([]any)
	for _, value := range formats {
		format, _ := value.(map[string]any)
		values, err := searchStrings(format["workFormatsElement"])
		if err != nil {
			return item, fmt.Errorf("HH search vacancy %s has invalid work formats: %w", item.ID, err)
		}
		item.WorkFormats = append(item.WorkFormats, values...)
	}
	return item, nil
}

func searchEmployer(data Object) (domain.SearchEntity, error) {
	company, _ := data["company"].(map[string]any)
	name, _ := company["visibleName"].(string)
	if name == "" {
		name, _ = company["name"].(string)
	}
	properties, _ := data["vacancyProperties"].(map[string]any)
	states, _ := properties["calculatedStates"].(map[string]any)
	state, _ := states["HH"].(map[string]any)
	id := nativeID(company["id"])
	if name == "" || id == "" && state["anonymous"] != true {
		return domain.SearchEntity{}, fmt.Errorf("missing employer identity or explicit anonymous-publication flag")
	}
	return domain.SearchEntity{ID: id, Name: name}, nil
}

func searchStrings(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a string array, got %T", value)
	}
	values := make([]string, len(items))
	for i, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("expected string at index %d, got %T", i, item)
		}
		values[i] = text
	}
	return values, nil
}

func searchSameValues(value any, expected []string) bool {
	values, err := searchStrings(value)
	if err != nil {
		return false
	}
	want := slices.Clone(expected)
	slices.Sort(values)
	slices.Sort(want)
	return slices.Equal(values, want)
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

func searchCompensation(value Object) Object {
	result := Object{}
	for _, key := range []string{"from", "to", "currencyCode", "gross", "mode"} {
		if v, exists := value[key]; exists {
			result[key] = v
		}
	}
	return result
}
