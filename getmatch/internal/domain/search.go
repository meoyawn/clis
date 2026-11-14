package domain

import (
	"fmt"
	"strings"
)

type VacancySearchOptions struct {
	Specializations []string
	Skills          []string
	Remote          bool
	Salary          int
	Levels          []Level
	Page            int
	PerPage         int
}

func (o VacancySearchOptions) Validate() error {
	for _, slug := range o.Specializations {
		if strings.TrimSpace(slug) == "" {
			return fmt.Errorf("invalid specialization %q: expected a nonempty Getmatch slug", slug)
		}
	}
	for _, skill := range o.Skills {
		if strings.TrimSpace(skill) == "" {
			return fmt.Errorf("invalid skill %q: expected a nonempty key-skill tag", skill)
		}
	}
	for _, level := range o.Levels {
		if _, err := NewLevelFromValue(level.String()); err != nil {
			return err
		}
		if level == AllLevels() {
			return fmt.Errorf("invalid search level %q: omit --level to search all levels", level.String())
		}
	}
	switch o.Salary {
	case 0, 150000, 200000, 250000, 350000:
	default:
		return fmt.Errorf("invalid salary %d: expected 0, 150000, 200000, 250000 or 350000", o.Salary)
	}
	if o.PerPage < 1 || o.PerPage > 100 {
		return fmt.Errorf("invalid per-page %d: expected 1 to 100", o.PerPage)
	}
	// Leave room for the next page and its offset without overflowing an int.
	if o.Page < 0 || o.Page > int(^uint(0)>>1)/o.PerPage-1 {
		return fmt.Errorf("invalid page %d: expected a nonnegative page with a representable next offset", o.Page)
	}
	return nil
}

type VacancySearchResult struct {
	URL               string              `json:"url"`
	TotalCandidates   int                 `json:"total_candidates"`
	Page              int                 `json:"page"`
	NextPage          *int                `json:"next_page,omitempty"`
	ScannedCandidates int                 `json:"scanned_candidates"`
	Vacancies         []VacancySearchItem `json:"vacancies"`
}

type VacancySearchItem struct {
	ID           string                    `json:"id"`
	URL          string                    `json:"url"`
	Title        string                    `json:"title"`
	Employer     SearchEmployer            `json:"employer"`
	Compensation VacancySearchCompensation `json:"compensation"`
	Published    string                    `json:"published"`
	Locations    []SearchLocation          `json:"locations"`
	KeySkills    []string                  `json:"key_skills,omitempty"`
}

type SearchEmployer struct {
	ID   *int   `json:"id,omitempty"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type SearchLocation struct {
	Label   string `json:"label"`
	Format  string `json:"format"`
	Exclude bool   `json:"exclude"`
}

type VacancySearchCompensation struct {
	From      *int    `json:"from,omitempty"`
	To        *int    `json:"to,omitempty"`
	Currency  *string `json:"currency,omitempty"`
	Taxes     *string `json:"taxes,omitempty"`
	Hidden    bool    `json:"hidden"`
	Total     bool    `json:"total"`
	Estimated bool    `json:"estimated"`
}
