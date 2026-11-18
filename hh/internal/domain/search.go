package domain

import (
	"fmt"
	"strconv"
	"strings"
)

type VacancySearchOptions struct {
	Text             string
	Skills           []string
	Remote           bool
	Areas            []string
	Employers        []string
	ProfessionalArea string
	Page             int
	PerPage          int
}

func (o VacancySearchOptions) Validate() error {
	if strings.TrimSpace(o.Text) == "" && len(o.Skills) == 0 && len(o.Employers) == 0 {
		return fmt.Errorf("vacancy search requires --text QUERY or --skill NAME or --employer ID")
	}
	for _, skill := range o.Skills {
		if strings.TrimSpace(skill) == "" {
			return fmt.Errorf("invalid skill %q: expected a nonempty key-skill tag", skill)
		}
	}
	for _, area := range o.Areas {
		id, err := strconv.ParseUint(area, 10, 64)
		if err != nil || id == 0 {
			return fmt.Errorf("invalid area %q: expected a positive HH area ID", area)
		}
	}
	for _, employer := range o.Employers {
		id, err := strconv.ParseUint(employer, 10, 64)
		if err != nil || id == 0 {
			return fmt.Errorf("invalid employer %q: expected a positive HH employer ID", employer)
		}
	}
	if o.ProfessionalArea != "" {
		id, err := strconv.ParseUint(o.ProfessionalArea, 10, 64)
		if err != nil || id == 0 {
			return fmt.Errorf("invalid professional-area %q: expected a positive HH category ID", o.ProfessionalArea)
		}
	}
	if o.PerPage < 1 || o.PerPage > 100 {
		return fmt.Errorf("invalid per-page %d: expected 1 to 100", o.PerPage)
	}
	if o.Page < 0 || o.Page > 1999/o.PerPage {
		return fmt.Errorf("invalid page %d: zero-based candidate page must start within HH's first 2000 results", o.Page)
	}
	return nil
}

// QueryText preserves explicit HH expressions. Skill names alone form a broad
// discovery query; the service separately checks the actual vacancy tags.
func (o VacancySearchOptions) QueryText() string {
	if strings.TrimSpace(o.Text) != "" {
		return o.Text
	}
	escape := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	terms := make([]string, len(o.Skills))
	for i, skill := range o.Skills {
		terms[i] = `"` + escape.Replace(strings.TrimSpace(skill)) + `"`
	}
	return strings.Join(terms, " OR ")
}

type VacancySearchResult struct {
	URL               string                  `json:"url"`
	TotalCandidates   int                     `json:"total_candidates"`
	Page              int                     `json:"page"`
	NextPage          *int                    `json:"next_page,omitempty"`
	ScannedCandidates int                     `json:"scanned_candidates"`
	Vacancies         []VacancySearchItem     `json:"vacancies"`
	ProfessionalArea  *SearchProfessionalArea `json:"professional_area,omitempty"`
}

type SearchProfessionalArea struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	RoleIDs []string `json:"role_ids"`
}

type VacancySearchItem struct {
	ID           string         `json:"id"`
	URL          string         `json:"url"`
	Title        string         `json:"title"`
	Employer     SearchEntity   `json:"employer"`
	Area         SearchEntity   `json:"area"`
	Compensation map[string]any `json:"compensation,omitempty"`
	Published    string         `json:"published,omitempty"`
	WorkFormats  []string       `json:"work_formats,omitempty"`
	KeySkills    []string       `json:"key_skills,omitempty"`
}

type SearchEntity struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}
