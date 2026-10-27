package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	http "github.com/bogdanfinn/fhttp"
	"github.com/meoyawn/clis/santehnika/internal/domain"
)

// Availability uses the public delivery calculation request observed in Chrome.
func (c *Client) Availability(ctx context.Context, apiID string) (*domain.AvailabilityResult, error) {
	id, err := strconv.ParseUint(apiID, 10, 64)
	if err != nil || id == 0 {
		return nil, fmt.Errorf("invalid api_id %q; use a positive numeric api_id from search/list", apiID)
	}
	apiID = strconv.FormatUint(id, 10)
	q := url.Values{
		"location":       {"Казань"},
		"product_id[]":   {apiID},
		"set_product_id": {apiID},
	}
	target := c.origin + "/ajax/react/deliveryCalculation/?" + q.Encode()
	headers := http.Header{
		"Accept":                    {"application/json, text/plain, */*"},
		"Sec-Fetch-Mode":            {"cors"},
		"Sec-Fetch-Dest":            {"empty"},
		"Sec-Fetch-User":            nil,
		"Upgrade-Insecure-Requests": nil,
		"Priority":                  {"u=1, i"},
	}
	body, err := c.fetch(ctx, http.MethodGet, target, "", nil, headers)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("delivery calculation returned invalid JSON")
	}
	return &domain.AvailabilityResult{URL: target, ProductAPIID: apiID, City: "Казань", Availability: body, HTTPRequests: c.requests}, nil
}
