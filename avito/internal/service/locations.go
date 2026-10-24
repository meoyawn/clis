package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const locationsPath = "/web/1/slocations"

type Location struct {
	ID   int64
	Name string
}

func ValidateCity(city string) error {
	if strings.TrimSpace(city) == "" || len(city) > 256 || strings.ContainsFunc(city, unicode.IsControl) {
		return fmt.Errorf("city %q must contain 1–256 bytes of nonblank text without control characters", truncate(city, 100))
	}
	return nil
}

func (c *Client) ResolveCity(ctx context.Context, city string, auth RequestOptions) (Location, error) {
	if err := ValidateCity(city); err != nil {
		return Location{}, err
	}
	city = strings.TrimSpace(city)
	target := &url.URL{Scheme: "https", Host: "www.avito.ru", Path: locationsPath}
	target.RawQuery = url.Values{
		"q": {city}, "limit": {"10"},
	}.Encode()
	request, err := c.Prepare(ctx, target, auth, false)
	if err != nil {
		return Location{}, err
	}
	response, err := c.Do(request)
	if err != nil {
		return Location{}, fmt.Errorf("resolve city %q: %w", city, err)
	}
	return parseCity(response.Body, city)
}

func parseCity(body []byte, city string) (Location, error) {
	var payload struct {
		Result *struct {
			Locations []struct {
				ID     int64             `json:"id"`
				Names  map[string]string `json:"names"`
				Parent struct {
					Names map[string]string `json:"names"`
				} `json:"parent"`
			} `json:"locations"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Location{}, fmt.Errorf("decode city lookup JSON: %w", err)
	}
	if payload.Result == nil || payload.Result.Locations == nil {
		return Location{}, fmt.Errorf("city lookup has no result.locations; it may be a challenge, login response, or changed schema")
	}
	var match Location
	var candidates []string
	seen := make(map[int64]bool)
	for _, location := range payload.Result.Locations {
		name := location.Names["1"]
		if location.ID <= 0 || ValidateCity(name) != nil {
			return Location{}, fmt.Errorf("city lookup returned an invalid location with ID %d", location.ID)
		}
		if !strings.EqualFold(name, city) || seen[location.ID] {
			continue
		}
		seen[location.ID] = true
		match = Location{ID: location.ID, Name: name}
		candidates = append(candidates, fmt.Sprintf("%q (%q, ID %d)", name, location.Parent.Names["1"], location.ID))
	}
	switch len(candidates) {
	case 0:
		return Location{}, fmt.Errorf("city %q was not found; use its full Russian name or search with --location-id ID", city)
	case 1:
		return match, nil
	default:
		return Location{}, fmt.Errorf("city %q is ambiguous: %s; search with --location-id ID to choose one", city, strings.Join(candidates, "; "))
	}
}
