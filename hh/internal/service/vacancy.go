package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

type Vacancy struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	Title         string   `json:"title"`
	Employer      Object   `json:"employer"`
	Compensation  Object   `json:"compensation"`
	ContactName   string   `json:"contact_name,omitempty"`
	Description   string   `json:"description"`
	Published     string   `json:"published"`
	Archived      bool     `json:"archived"`
	Closed        bool     `json:"closed"`
	WorkFormats   any      `json:"work_formats"`
	KeySkills     []string `json:"key_skills,omitempty"`
	ResponseState Object   `json:"response_state,omitempty"`
}

type vacancyPage struct {
	data          Object
	responseState Object
}

func (c *Client) readVacancyPage(ctx context.Context, id VacancyID) (*vacancyPage, error) {
	body, err := c.request(ctx, http.MethodGet, "/vacancy/"+id.String(), nil, nil, true)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(body)
	if err != nil {
		return nil, err
	}
	view, _ := state["vacancyView"].(map[string]any)
	full, _ := view["vacancyFull"].(map[string]any)
	data, _ := full["vacancy"].(map[string]any)
	if nativeID(data["vacancyId"]) != id.String() {
		return nil, fmt.Errorf("vacancy identity mismatch: requested %s, got %v; stopped", id, data["vacancyId"])
	}
	responses, _ := state["applicantVacancyResponseStatuses"].(map[string]any)
	response, _ := responses[id.String()].(map[string]any)
	return &vacancyPage{data: data, responseState: response}, nil
}

func (c *Client) Vacancy(ctx context.Context, id VacancyID) (*Vacancy, error) {
	page, err := c.readVacancyPage(ctx, id)
	if err != nil {
		return nil, err
	}
	data := page.data
	company, ok := data["company"].(map[string]any)
	if !ok || company["id"] == nil || company["name"] == nil {
		return nil, fmt.Errorf("vacancy %s has no verified employer identity", id)
	}
	description, _ := data["description"].(string)
	text, err := PrintText([]byte(`<div id="HH-React-Root">` + description + `</div>`))
	if err != nil {
		return nil, err
	}
	name, _ := data["name"].(string)
	if name == "" || text == "" {
		return nil, fmt.Errorf("vacancy %s has empty title or description; stopped", id)
	}
	compensation, _ := data["compensation"].(map[string]any)
	status, _ := data["status"].(map[string]any)
	contact, _ := data["contactInfo"].(map[string]any)
	contactName, _ := contact["fio"].(string)
	published, _ := data["publicationTimeIso"].(string)
	archived, _ := status["archived"].(bool)
	closed, _ := data["closedForApplicants"].(bool)
	var keySkills []string
	if value, exists := data["keySkills"]; exists {
		keySkills, err = searchStrings(value)
		if err != nil {
			return nil, fmt.Errorf("vacancy %s has invalid keySkills: %w", id, err)
		}
	}
	return &Vacancy{ID: id.String(), URL: c.base.String() + "/vacancy/" + id.String(), Title: name, Employer: Object{"id": company["id"], "name": company["name"]}, Compensation: compensation, ContactName: contactName, Description: text, Published: published, Archived: archived, Closed: closed, WorkFormats: data["workFormats"], KeySkills: keySkills, ResponseState: page.responseState}, nil
}

func nativeID(value any) string {
	switch v := value.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return v
	default:
		return ""
	}
}

// ResponseOptions fetches the recorded GET preflight only. A quickResponse
// result contains a proposed POST body; it must never be executed implicitly.
func (c *Client) ResponseOptions(ctx context.Context, id VacancyID) (Object, error) {
	body, err := c.request(ctx, http.MethodGet, "/applicant/vacancy_response/popup", url.Values{"isTest": {"no"}, "lux": {"true"}, "withoutTest": {"no"}, "vacancyId": {id.String()}}, nil, false)
	if err != nil {
		return nil, err
	}
	var options Object
	if err := json.Unmarshal(body, &options); err != nil {
		return nil, fmt.Errorf("decode vacancy %s response options: %w", id, err)
	}
	if _, ok := options["type"].(string); !ok {
		return nil, fmt.Errorf("vacancy %s response options have no type; stopped", id)
	}
	// Scripts are page assets, never application instructions or data to execute.
	delete(options, "inline_script")
	delete(options, "css_links")
	return options, nil
}
