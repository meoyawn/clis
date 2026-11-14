package service

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"net/url"
	"reflect"
	"strconv"

	"github.com/meoyawn/clis/hh/internal/domain"
)

type SavedSearch struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	EmailActive bool   `json:"email_active"`
	Parameters  Object `json:"parameters"`
}

type SavedSearchList struct {
	URL           string        `json:"url"`
	Email         string        `json:"email"`
	EmailVerified bool          `json:"email_verified"`
	Searches      []SavedSearch `json:"searches"`
	Total         int           `json:"total"`
}

type SavedSearchResult struct {
	Created           bool         `json:"created"`
	Verified          bool         `json:"verified"`
	QueryURL          string       `json:"query_url"`
	NotificationEmail string       `json:"notification_email"`
	Search            *SavedSearch `json:"search,omitempty"`
}

// SavedSearches reads the native favorites/searches state observed on 14 Nov 2026.
// Private subscription/deactivation tokens are deliberately omitted from output.
func (c *Client) SavedSearches(ctx context.Context) (*SavedSearchList, error) {
	query := url.Values{"tab": {"searches"}}
	body, err := c.request(ctx, http.MethodGet, "/applicant/favorites", query, nil, true)
	if err != nil {
		return nil, err
	}
	state, err := DecodeState(body)
	if err != nil {
		return nil, err
	}
	data, ok := state["favoritesVacancySavedSearches"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("saved searches page has no favoritesVacancySavedSearches; authentication or page contract changed")
	}
	rows, rowsOK := data["savedSearch"].([]any)
	total, totalOK := data["totalItems"].(float64)
	if !rowsOK || !totalOK || total != float64(len(rows)) || data["paging"] != nil {
		return nil, fmt.Errorf("saved searches listing is incomplete or paginated; no save attempted")
	}
	result := &SavedSearchList{
		URL:      c.base.ResolveReference(&url.URL{Path: "/applicant/favorites", RawQuery: query.Encode()}).String(),
		Searches: make([]SavedSearch, 0, len(rows)), Total: len(rows),
	}
	result.Email, _ = data["email"].(string)
	result.EmailVerified, _ = data["emailVerified"].(bool)
	seen := map[string]bool{}
	for _, value := range rows {
		row, _ := value.(map[string]any)
		id, _ := row["id"].(string)
		name, nameOK := row["name"].(string)
		args, argsOK := row["searchArgs"].(string)
		parameters, paramsOK := row["param"].(map[string]any)
		active, activeOK := row["emailActive"].(bool)
		number, numberErr := strconv.ParseUint(id, 10, 64)
		if numberErr != nil || number == 0 || seen[id] || !nameOK || !argsOK || !paramsOK || !activeOK {
			return nil, fmt.Errorf("invalid or duplicate saved-search record %q; no save attempted", id)
		}
		seen[id] = true
		argsQuery, err := url.ParseQuery(html.UnescapeString(args))
		if err != nil || argsQuery.Get("saved_search_id") != id {
			return nil, fmt.Errorf("saved-search %s has invalid searchArgs or a mismatched saved_search_id", id)
		}
		searchURL := c.base.ResolveReference(&url.URL{Path: "/search/vacancy", RawQuery: argsQuery.Encode()}).String()
		result.Searches = append(result.Searches, SavedSearch{ID: id, Name: name, URL: searchURL, EmailActive: active, Parameters: parameters})
	}
	return result, nil
}

func savedSearchMatches(search SavedSearch, options domain.SavedSearchOptions) bool {
	u, err := url.Parse(search.URL)
	if err != nil {
		return false
	}
	query := u.Query()
	if query.Get("text") != options.Text || len(query["text"]) != 1 {
		return false
	}
	if options.Remote {
		if !reflect.DeepEqual(query["work_format"], []string{"REMOTE"}) {
			return false
		}
	} else if len(query["work_format"]) != 0 {
		return false
	}
	for key, values := range query {
		switch key {
		case "text", "work_format", "items_on_page", "saved_search_id", "no_magic", "enable_snippets", "ored_clusters":
		case "order_by":
			if !reflect.DeepEqual(values, []string{"relevance"}) {
				return false
			}
		default:
			// Additional location, salary or other filters are a different search.
			return false
		}
	}
	return true
}

// SaveSearch uses the native POST captured with browser mutations blocked:
// /vacancysavedsearch/save?<criteria>, JSON {"email": <verified HH account email>}.
// A fresh list, not the POST status, confirms the exact saved criteria.
func (c *Client) SaveSearch(ctx context.Context, options domain.SavedSearchOptions, apply bool) (*SavedSearchResult, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	before, err := c.SavedSearches(ctx)
	if err != nil {
		return nil, err
	}
	query := url.Values{"text": {options.Text}, "items_on_page": {"20"}, "enable_snippets": {"true"}, "ored_clusters": {"true"}}
	if options.Remote {
		query.Set("work_format", "REMOTE")
	}
	result := &SavedSearchResult{
		QueryURL:          c.base.ResolveReference(&url.URL{Path: "/search/vacancy", RawQuery: query.Encode()}).String(),
		NotificationEmail: before.Email,
	}
	for _, search := range before.Searches {
		if savedSearchMatches(search, options) {
			result.Search, result.Verified = &search, true
			return result, nil
		}
	}
	if !apply {
		return result, nil
	}
	address, err := mail.ParseAddress(before.Email)
	if err != nil || address.Address != before.Email || !before.EmailVerified {
		return nil, fmt.Errorf("saved search requires the verified native account email, got %q (verified=%t); no write attempted", before.Email, before.EmailVerified)
	}
	if _, err := c.request(ctx, http.MethodPost, "/vacancysavedsearch/save", query, Object{"email": before.Email}, false); err != nil {
		return nil, fmt.Errorf("save search: %w; inspect saved-searches list before retrying", err)
	}
	after, err := c.SavedSearches(ctx)
	if err != nil {
		return nil, fmt.Errorf("saved-search readback failed: %w; inspect saved-searches list before retrying", err)
	}
	current := make(map[string]SavedSearch, len(after.Searches))
	for _, search := range after.Searches {
		current[search.ID] = search
	}
	for _, previous := range before.Searches {
		if !reflect.DeepEqual(previous, current[previous.ID]) {
			return nil, fmt.Errorf("existing saved search %s changed during save; inspect saved-searches list before retrying", previous.ID)
		}
		delete(current, previous.ID)
	}
	if len(current) != 1 {
		return nil, fmt.Errorf("save produced %d identifiable new searches; inspect saved-searches list before retrying", len(current))
	}
	for _, search := range current {
		if !savedSearchMatches(search, options) {
			return nil, fmt.Errorf("new saved search %s does not match requested criteria; inspect saved-searches list before retrying", search.ID)
		}
		result.Search = &search
	}
	result.Created, result.Verified = true, true
	return result, nil
}
