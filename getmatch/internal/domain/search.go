package domain

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type VacancySearchOptions struct {
	Specializations []string
	Skills          []string
	// Locations are native l values; Getmatch returns their union.
	Locations []string
	Salary    int
	// Period is the native pa value; empty means all.
	Period  string
	Levels  []Level
	Page    int
	PerPage int
	// S is the native s value, forwarded verbatim from a pasted search URL.
	S string
}

var (
	SearchLocations = []string{"moscow", "saints_p", "regions_ru", "relocate", "remote"}
	SearchPeriods   = []string{"all", "1d", "3d", "7d", "30d"}
)

// RemoteOnly reports whether every returned vacancy must offer remote work.
func (o VacancySearchOptions) RemoteOnly() bool {
	return len(o.Locations) > 0 && !slices.ContainsFunc(o.Locations, func(l string) bool { return l != "remote" })
}

// ParseVacancySearchURL maps a getmatch.ru/vacancies URL onto search options.
// Unknown parameters are rejected rather than silently dropped.
func ParseVacancySearchURL(raw string) (VacancySearchOptions, error) {
	options := VacancySearchOptions{PerPage: 20}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "getmatch.ru" && u.Host != "www.getmatch.ru" || strings.TrimSuffix(u.Path, "/") != "/vacancies" || u.User != nil {
		return options, fmt.Errorf("invalid search URL %q: expected a https://getmatch.ru/vacancies search URL", raw)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return options, fmt.Errorf("invalid search URL query: %w", err)
	}
	for key, values := range query {
		single := func() (string, error) {
			if len(values) != 1 {
				return "", fmt.Errorf("search URL repeats %q", key)
			}
			return values[0], nil
		}
		var value string
		switch key {
		case "sp":
			options.Specializations = values
		case "l":
			options.Locations = values
		case "se":
			for _, v := range values {
				level, err := NewLevelFromValue(v)
				if err != nil {
					return options, err
				}
				options.Levels = append(options.Levels, level)
			}
		case "p":
			if value, err = single(); err != nil {
				return options, err
			}
			page, err := strconv.Atoi(value)
			if err != nil || page < 1 {
				return options, fmt.Errorf("invalid search URL p=%q: expected a positive page", value)
			}
			options.Page = page - 1
		case "sa":
			if value, err = single(); err != nil {
				return options, err
			}
			if value != "any" {
				if options.Salary, err = strconv.Atoi(value); err != nil {
					return options, fmt.Errorf("invalid search URL sa=%q", value)
				}
			}
		case "pa":
			if options.Period, err = single(); err != nil {
				return options, err
			}
		case "s":
			if options.S, err = single(); err != nil {
				return options, err
			}
		default:
			return options, fmt.Errorf("unsupported search URL parameter %q", key)
		}
	}
	return options, options.Validate()
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
	for _, location := range o.Locations {
		if !slices.Contains(SearchLocations, location) {
			return fmt.Errorf("invalid location %q, allowed values: %v", location, SearchLocations)
		}
	}
	if o.Period != "" && !slices.Contains(SearchPeriods, o.Period) {
		return fmt.Errorf("invalid period %q, allowed values: %v", o.Period, SearchPeriods)
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
