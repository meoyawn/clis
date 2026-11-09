package domain

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Filters struct {
	Directions  []string `json:"directions"`
	WorkFormats []string `json:"work_formats"`
}

func (f Filters) SourceURL() string {
	query := url.Values{}
	query.Set("directions", strings.Join(f.Directions, ","))
	query.Set("work_formats", strings.Join(f.WorkFormats, ","))
	return "https://ozon.tech/vacancies/?" + query.Encode()
}

type Description struct {
	Quote  json.RawMessage `json:"quote"`
	Blocks json.RawMessage `json:"blocks"`
	HTML   string          `json:"html"`
}

type Vacancy struct {
	ID                  string       `json:"id"`
	Slug                string       `json:"slug"`
	Title               string       `json:"title"`
	Team                string       `json:"team"`
	City                string       `json:"city"`
	Level               string       `json:"level"`
	WorkFormats         []string     `json:"work_formats"`
	InternalUUID        string       `json:"internal_uuid"`
	VacancyType         string       `json:"vacancy_type"`
	ExternalLink        string       `json:"external_link,omitempty"`
	IsTrainee           bool         `json:"is_trainee"`
	Tech                string       `json:"tech,omitempty"`
	HHID                string       `json:"hh_id,omitempty"`
	Description         *Description `json:"description,omitempty"`
	DescriptionMarkdown string       `json:"description_markdown,omitempty"`
	IsDescriptionEdited bool         `json:"is_description_edited,omitempty"`
	URL                 string       `json:"url"`
	DetailsCheckedAt    *time.Time   `json:"details_checked_at,omitempty"`
}

var idPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid vacancy ID %q; expected a positive decimal ID", id)
	}
	return nil
}

func (v *Vacancy) Validate() error {
	if err := ValidateID(v.ID); err != nil {
		return err
	}
	if !uuidPattern.MatchString(v.InternalUUID) || !slugPattern.MatchString(v.Slug) || strings.TrimSpace(v.Title) == "" || v.WorkFormats == nil {
		return fmt.Errorf("incomplete vacancy %q: expected a UUID, slug, title and work_formats array", v.ID)
	}
	v.URL = "https://ozon.tech/vacancies/" + v.InternalUUID + "-" + v.Slug + "/"
	return nil
}

type Catalog struct {
	SourceURL    string    `json:"source_url"`
	SyncedAt     time.Time `json:"synced_at"`
	Filters      Filters   `json:"filters"`
	Count        int       `json:"count"`
	PagesFetched int       `json:"pages_fetched"`
	Vacancies    []Vacancy `json:"vacancies"`
}
