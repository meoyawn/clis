package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const siteOrigin = "https://www.invitro.ru"
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"
const maxResponseBytes = 16 << 20
const requestBudget = 3
const pageSize = 20
const maxPage = 1000000

type client struct {
	http         *http.Client
	base         *url.URL
	userAgent    string
	requests     int
	requestLimit int
}

func newClient(origin, userAgent string) (*client, error) {
	base, err := url.Parse(origin)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, fmt.Errorf("invalid origin %q: expected an HTTP(S) origin", origin)
	}
	if strings.TrimSpace(userAgent) == "" || strings.ContainsAny(userAgent, "\r\n") {
		return nil, fmt.Errorf("user-agent must be nonempty and contain no newlines")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		// No reused connections, implicit retries, or redirects.
		DisableKeepAlives: true,
	}
	return &client{base: base, userAgent: userAgent, requestLimit: requestBudget, http: &http.Client{
		Transport: transport, Timeout: 45 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *client) request(ctx context.Context, path string, values url.Values, payload any, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.requests >= c.requestLimit {
		return fmt.Errorf("HTTP request budget of %d exhausted", c.requestLimit)
	}
	if !strings.HasPrefix(path, "/golk/") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("invalid API path %q", path)
	}
	target := *c.base
	target.Path, target.RawQuery = path, values.Encode()
	var body io.Reader
	method := http.MethodGet
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body, method = bytes.NewReader(data), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	req.Header.Set("Web", "true")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.requests++
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("anonymous catalog request %s was rejected (HTTP %d); the site may be applying access checks; verify access in Chrome or try --user-agent matching that browser", path, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("catalog resource %s was not found (HTTP 404); check the ID, city, and --type, or supply the full product URL", path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("request %s returned HTTP %d (redirects and retries are disabled)", path, resp.StatusCode)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("response %s exceeds %d bytes", path, maxResponseBytes)
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		return fmt.Errorf("request %s returned non-JSON content; possible site-verification page", path)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("request %s returned null instead of catalog data", path)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func (c *client) resolveCity(ctx context.Context, slug string) (cityInfo, error) {
	if err := validateCity(slug); err != nil {
		return cityInfo{}, err
	}
	var response struct {
		Cities []cityInfo `json:"cities"`
	}
	if err := c.request(ctx, "/golk/addresses/api/v1/cities", url.Values{"q": {slug}}, nil, &response); err != nil {
		return cityInfo{}, fmt.Errorf("resolve city %q: %w", slug, err)
	}
	for _, city := range response.Cities {
		if city.Slug == slug && uuidPattern.MatchString(city.ID) {
			return city, nil
		}
	}
	return cityInfo{}, fmt.Errorf("city %q was not found by the site; use its exact city URL slug", slug)
}

func searchURL(query, city string, kind ProductKind, page int, filters searchFilters) string {
	v := filters.values()
	v.Set("search", query)
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	cityPath := city + "/"
	if city == "moscow" {
		cityPath = ""
	}
	return siteOrigin + "/analizes/" + kind.webPath() + "/" + cityPath + "?" + v.Encode()
}

func (c *client) Search(ctx context.Context, query, slug string, kind ProductKind, page int, filters searchFilters, includeFilters bool) (*searchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("query %q must not be empty", query)
	}
	if page < 1 || page > maxPage {
		return nil, fmt.Errorf("page %d must be between 1 and %d", page, maxPage)
	}
	if _, err := NewProductKindFromValue(kind.value); err != nil {
		return nil, err
	}
	if includeFilters {
		c.requestLimit = requestBudget + 1
	}
	city, err := c.resolveCity(ctx, slug)
	if err != nil {
		return nil, err
	}
	var found struct {
		Group string   `json:"group"`
		IDs   []string `json:"ids"`
	}
	if err := c.request(ctx, "/golk/search/api/v1/search/"+kind.value, url.Values{"q": {query}, "cityId": {city.ID}}, nil, &found); err != nil {
		return nil, err
	}
	if found.Group != kind.value {
		return nil, fmt.Errorf("search returned group %q instead of %q", found.Group, kind.value)
	}
	for _, id := range found.IDs {
		if !uuidPattern.MatchString(id) {
			return nil, fmt.Errorf("search returned invalid catalog ID %q", id)
		}
	}
	result := &searchResult{Query: query, City: slug, CityID: city.ID, Type: kind.value, Filters: map[string]string{}, Page: page, TotalPages: 1, Results: []product{}, SourceURL: searchURL(query, slug, kind, page, filters)}
	for key, values := range filters.values() {
		result.Filters[key] = values[0]
	}
	if len(found.IDs) > 0 {
		values := filters.values()
		values.Set("cityID", city.ID)
		values.Set("limit", strconv.Itoa(pageSize))
		values.Set("offset", strconv.Itoa((page-1)*pageSize))
		var response struct {
			Total  *int         `json:"total"`
			Limit  *int         `json:"limit"`
			Offset *int         `json:"offset"`
			Data   []apiProduct `json:"data"`
		}
		if err := c.request(ctx, "/golk/tests/api/v1/"+kind.apiPath()+"/by-ids", values, struct {
			IDs []string `json:"ids"`
		}{found.IDs}, &response); err != nil {
			return nil, err
		}
		if response.Total == nil || *response.Total < 0 || response.Limit == nil || *response.Limit != pageSize || response.Offset == nil || *response.Offset != (page-1)*pageSize {
			return nil, fmt.Errorf("catalog returned invalid pagination metadata for page %d", page)
		}
		result.Total = *response.Total
		result.TotalPages = max(1, (result.Total+pageSize-1)/pageSize)
		if page > result.TotalPages {
			return nil, fmt.Errorf("page %d exceeds available pages (%d)", page, result.TotalPages)
		}
		expected := min(pageSize, max(0, result.Total-(page-1)*pageSize))
		if len(response.Data) != expected {
			return nil, fmt.Errorf("catalog returned %d products, expected %d for page %d", len(response.Data), expected, page)
		}
		allowed := make(map[string]bool, len(found.IDs))
		for _, id := range found.IDs {
			allowed[id] = true
		}
		seen := make(map[string]bool, len(response.Data))
		for _, raw := range response.Data {
			if !allowed[raw.ID] || seen[raw.ID] {
				return nil, fmt.Errorf("catalog returned unexpected or duplicate product %q", raw.ID)
			}
			seen[raw.ID] = true
			item, err := normalizeProduct(raw, slug, kind)
			if err != nil {
				return nil, err
			}
			result.Results = append(result.Results, item)
		}
	}
	if page > result.TotalPages {
		return nil, fmt.Errorf("page %d exceeds available pages (%d)", page, result.TotalPages)
	}
	result.Count = len(result.Results)
	result.HasMore = page < result.TotalPages
	if result.HasMore {
		next := page + 1
		result.NextPage = &next
		result.NextURL = searchURL(query, slug, kind, next, filters)
	}
	if includeFilters {
		catalog, err := c.fetchFilters(ctx, kind)
		if err != nil {
			return nil, err
		}
		result.AvailableFilters = &catalog
	}
	result.FetchedAt = time.Now().UTC()
	result.HTTPRequests = c.requests
	return result, nil
}

func (c *client) fetchFilters(ctx context.Context, kind ProductKind) (filterCatalog, error) {
	var catalog filterCatalog
	if err := c.request(ctx, "/golk/tests/api/v1/"+kind.apiPath()+"/filters", nil, nil, &catalog); err != nil {
		return nil, fmt.Errorf("fetch available filters: %w", err)
	}
	for name, definition := range catalog {
		if !uuidPattern.MatchString(definition.ID) || definition.Label == "" || definition.Type == "" {
			return nil, fmt.Errorf("invalid definition for filter %q", name)
		}
		// Only advertise flags that this CLI implements for the selected type.
		definition.CLIFlag = ""
		switch name {
		case "available_at_home":
			if kind != Checkups() {
				definition.CLIFlag = "--available-at-home"
			}
		case "is_promo":
			definition.CLIFlag = "--is-promo"
		case "deadline":
			if kind != Checkups() {
				definition.CLIFlag = "--deadline"
			}
		}
		catalog[name] = definition
	}
	return catalog, nil
}

func (c *client) Show(ctx context.Context, ref productRef, slug string, kind ProductKind, full bool) (*detailResult, error) {
	if !validID.MatchString(ref.ID) && !uuidPattern.MatchString(ref.ID) {
		return nil, fmt.Errorf("invalid product id %q", ref.ID)
	}
	if _, err := NewProductKindFromValue(kind.value); err != nil {
		return nil, err
	}
	city, err := c.resolveCity(ctx, slug)
	if err != nil {
		return nil, err
	}
	id := ref.ID
	if validID.MatchString(id) {
		var mapping struct {
			Product struct {
				UUID string `json:"uuid"`
			} `json:"product"`
		}
		if err := c.request(ctx, "/golk/tests/api/v1/mappers/product/"+id, nil, nil, &mapping); err != nil {
			return nil, err
		}
		id = mapping.Product.UUID
		if !uuidPattern.MatchString(id) {
			return nil, fmt.Errorf("site returned an invalid UUID for website ID %q", ref.ID)
		}
	}
	var data json.RawMessage
	if err := c.request(ctx, "/golk/tests/api/v1/"+kind.apiPath()+"/"+id, url.Values{"cityID": {city.ID}}, nil, &data); err != nil {
		return nil, err
	}
	var raw apiProduct
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode product %q: %w", ref.ID, err)
	}
	if raw.ID != id || (validID.MatchString(ref.ID) && strconv.FormatInt(raw.BitrixID, 10) != ref.ID) {
		return nil, fmt.Errorf("site returned product %q (website ID %d) instead of requested %q", raw.ID, raw.BitrixID, ref.ID)
	}
	item, err := normalizeProduct(raw, slug, kind)
	if err != nil {
		return nil, err
	}
	result := &detailResult{product: item, City: slug, CityID: city.ID, SourceURL: item.URL, FetchedAt: time.Now().UTC(), HTTPRequests: c.requests}
	if full {
		result.Source = data
		result.Sections = map[string]string{}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, fmt.Errorf("decode instruction fields: %w", err)
		}
		for key, value := range fields {
			if !strings.HasSuffix(key, "_markup") {
				continue
			}
			var markup string
			if err := json.Unmarshal(value, &markup); err != nil {
				return nil, fmt.Errorf("decode instruction %q: %w", key, err)
			}
			if text := plainText(markup); text != "" {
				result.Sections[strings.TrimSuffix(key, "_markup")] = text
			}
		}
	}
	return result, nil
}
