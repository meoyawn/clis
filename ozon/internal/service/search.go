package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

func (c *Client) Search(ctx context.Context, o SearchOptions, opt RequestOptions) (*SearchResult, error) {
	path, err := SearchPath(o)
	if err != nil {
		return nil, err
	}
	p, path, err := c.fetchSearch(ctx, path, opt)
	if err != nil {
		return nil, err
	}
	out, err := parseSearch(p, o, path)
	if err != nil {
		return nil, err
	}
	if o.IncludeFilters {
		out.SearchFacets = &SearchFacets{}
		facets, err := parseFilters(p, o.Query)
		switch {
		case errors.Is(err, errNoFilters):
			out.Warnings = append(out.Warnings, "Ozon did not supply filter facets on this page; use ozon filters with the same query and filters.")
		case err != nil:
			return nil, err
		default:
			out.Filters, out.Sorting = facets.Filters, facets.Sorting
		}
	}
	return out, nil
}

const maxSearchRedirects = 3

// Follow page redirects explicitly so every hop is checked and counted, while
// the HTTP client still refuses automatic redirects for all other requests.
func (c *Client) fetchSearch(ctx context.Context, path string, opt RequestOptions) (*page, string, error) {
	current, err := searchURL(path)
	if err != nil {
		return nil, path, err
	}
	if err := ctx.Err(); err != nil {
		return nil, path, err
	}
	path = current.String()
	target := entrypointURL(path)
	cookies, err := c.auth.cookiesFor(target)
	if err != nil {
		return nil, path, err
	}
	// Redirect cookies belong only to this invocation, never to the saved
	// browser session or another concurrent search using this client.
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, path, err
	}
	jar.SetCookies(target, cookies)
	cookiesFor := func(target *url.URL) ([]*http.Cookie, error) { return jar.Cookies(target), nil }
	visited := map[string]bool{searchRequestKey(current, target): true}
	var requests int
	var sources []string
	var warnings []string
	for redirects := 0; ; redirects++ {
		p, err := c.fetchPage(ctx, path, target, opt, cookiesFor)
		if p != nil {
			requests += p.meta.HTTPRequests
			sources = append(sources, p.meta.SourceURLs...)
			p.meta.HTTPRequests, p.meta.SourceURLs = requests, sources
			p.meta.Warnings = append(p.meta.Warnings, warnings...)
		}
		var redirect *pageRedirectError
		if !errors.As(err, &redirect) {
			return p, path, err
		}
		if redirects == maxSearchRedirects {
			return p, path, fmt.Errorf("%w: exceeded %d search redirects", err, maxSearchRedirects)
		}
		next, nextTarget, validationErr := searchRedirectURL(redirect.target)
		if validationErr == nil {
			validationErr = sameSearchRedirectConstraints(current.Query(), next.Query())
		}
		if validationErr != nil {
			return p, path, fmt.Errorf("%w: invalid search redirect: %w", err, validationErr)
		}
		if recommendationSearch(next.Query()) {
			return p, path, fmt.Errorf("%w: search redirect enters Ozon's recommendation feed", err)
		}
		if sourcePageNumber(current.String()) != sourcePageNumber(next.String()) {
			return p, path, fmt.Errorf("%w: search redirect changed the requested page", err)
		}
		key := searchRequestKey(next, nextTarget)
		if visited[key] {
			return p, path, fmt.Errorf("%w: search redirect loop", err)
		}
		visited[key] = true
		if !current.Query().Has("brand") && next.Query().Has("brand") {
			warnings = append(warnings, fmt.Sprintf("Ozon inferred brand=%s from the search query.", next.Query().Get("brand")))
		}
		jar.SetCookies(target, redirect.cookies)
		current, path = next, next.String()
		target = nextTarget
	}
}

func parseSearch(p *page, o SearchOptions, path string) (*SearchResult, error) {
	source, err := validateSearchResponse(p, path)
	if err != nil {
		return nil, err
	}
	criteria := source.Query()
	requested := sourcePageNumber(path)
	out := &SearchResult{Metadata: p.meta, Query: o.Query, Pagination: Pagination{Page: requested}, Results: []Product{}}
	if message, empty, err := emptySearch(p); err != nil {
		return nil, err
	} else if empty {
		zero := int64(0)
		out.Total = &zero
		out.Message = message
		return out, nil
	}
	if err := validateActiveFilters(p, criteria); err != nil {
		return nil, err
	}
	g, next, err := searchGrid(p)
	if err != nil {
		return nil, err
	}
	low, high, err := searchPriceRange(criteria)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	if page := integer(g["page"]); page == nil || *page != int64(requested) {
		return nil, fmt.Errorf("Ozon search grid does not identify requested page %d", requested)
	}
	items, ok := g["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("Ozon search grid is missing its items array")
	}
	for _, item := range items {
		product, err := parseProduct(obj(item))
		if err != nil {
			return nil, err
		}
		if low != nil && (product.Price == nil || product.Price.Amount < *low || product.Price.Amount > *high) {
			out.ExcludedCount++
			continue
		}
		if !seen[product.SKU] {
			out.Results = append(out.Results, product)
			seen[product.SKU] = true
		}
	}
	out.Count = len(out.Results)
	if out.ExcludedCount > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("Excluded %d results whose displayed price was outside the requested range or unavailable.", out.ExcludedCount))
	}
	if next != "" {
		u, e := searchURL(next)
		if e != nil {
			return nil, fmt.Errorf("invalid search continuation: %w", e)
		}
		if recommendationSearch(u.Query()) {
			out.Message = "Search matches ended; Ozon's recommendation continuation was omitted."
			return out, nil
		}
		if err := sameSearchScope(source, u); err != nil {
			return nil, fmt.Errorf("invalid search continuation: %w", err)
		}
		n, err := pageNumber(u.Query().Get("page"))
		if err != nil {
			return nil, err
		}
		if n <= requested {
			return nil, fmt.Errorf("Ozon returned a non-advancing search cursor")
		}
		out.HasMore = true
		out.NextPage = &n
		out.NextCursor = u.String()
	}
	return out, nil
}

func parseProduct(v object) (Product, error) {
	p := Product{SKU: str(v["sku"]), URL: publicURL(str(at(v, "action", "link"))), Images: []string{}}
	if p.SKU == "" {
		p.SKU = str(v["id"])
	}
	if !skuPattern.MatchString(p.SKU) {
		return p, fmt.Errorf("Ozon result has no valid SKU")
	}
	if p.URL == "" {
		p.URL = WebOrigin + "/product/" + p.SKU + "/"
	}
	for _, img := range arr(at(v, "tileImage", "items")) {
		p.Images = addUnique(p.Images, publicURL(str(at(img, "image", "link"))))
	}
	button := at(v, "multiButton", "ozonButton", "addToCart")
	p.DeliveryDay = text(at(button, "actionButton", "title"))
	p.MaxOrderQuantity = integer(at(button, "quantityButton", "maxItems"))
	for _, state := range arr(v["mainState"]) {
		kind := str(at(state, "type"))
		body := at(state, kind)
		switch kind {
		case "textDS":
			s := text(body)
			if str(at(state, "id")) == "name" || str(at(body, "testInfo", "automatizationId")) == "tile-name" {
				p.Title = s
			}
			if strings.Contains(strings.ToLower(s), "остал") {
				p.Quantity = textInteger(s)
				p.QuantitySource = "stock_label"
			}
		case "priceV2":
			for _, price := range arr(at(body, "price")) {
				if str(at(price, "textStyle")) == "PRICE" {
					p.Price = money(at(price, "text"))
				}
			}
		case "labelListV2":
			if str(at(body, "testInfo", "automatizationId")) != "tile-list-rating" {
				continue
			}
			for _, item := range arr(at(body, "items")) {
				s := text(at(item, "text"))
				if s == "" {
					continue
				}
				if strings.Contains(s, "отзыв") || strings.Contains(s, "оцен") {
					p.NumRatings = textInteger(s)
				} else if rating := textNumber(s); rating != nil && *rating <= 5 {
					p.Rating = rating
				}
			}
		}
	}
	if p.Title == "" {
		return p, fmt.Errorf("Ozon result SKU %s has no title; schema changed", p.SKU)
	}
	return p, nil
}

func (c *Client) Filters(ctx context.Context, o SearchOptions, opt RequestOptions) (*FiltersResult, error) {
	if o.Page != 1 || o.Cursor != "" {
		return nil, fmt.Errorf("filters requires page 1 without a cursor")
	}
	path, err := SearchPath(o)
	if err != nil {
		return nil, err
	}
	p, path, err := c.fetchSearch(ctx, path, opt)
	if err != nil {
		return nil, err
	}
	criteria, err := validateSearchResponse(p, path)
	if err != nil {
		return nil, err
	}
	if _, empty, err := emptySearch(p); err != nil {
		return nil, err
	} else if !empty {
		if err := validateActiveFilters(p, criteria.Query()); err != nil {
			return nil, err
		}
	}
	return parseFilters(p, o.Query)
}

var errNoFilters = errors.New("search response has no filters widget")

func parseFilters(p *page, query string) (*FiltersResult, error) {
	out := &FiltersResult{Metadata: p.meta, Query: query, Filters: []Filter{}, Sorting: []FilterOption{}}
	if message, empty, err := emptySearch(p); err != nil {
		return nil, err
	} else if empty {
		out.Message = message
		return out, nil
	}
	w, err := p.first("filtersDesktop")
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errNoFilters
	}
	sections, ok := w["sections"].([]any)
	if !ok {
		return nil, fmt.Errorf("Ozon filters widget has no sections")
	}
	for _, s := range sections {
		for _, raw := range arr(at(s, "filters")) {
			kind := str(at(raw, "type"))
			body := at(raw, kind)
			f := Filter{Key: str(at(raw, "key")), Type: kind, Title: text(at(body, "title")), Description: text(at(body, "description")), Options: []FilterOption{}}
			f.Selected = boolValue(at(body, "isSelected"))
			rangeBody := body
			optionsBody := body
			if kind == "multipleRangesFilter" {
				rangeBody = at(body, "rangeFilter")
				optionsBody = at(body, "checkboxesFilter")
				f.Title = text(at(rangeBody, "title"))
				f.Description = text(at(rangeBody, "description"))
			}
			f.Min = number(at(rangeBody, "minValue"))
			f.Max = number(at(rangeBody, "maxValue"))
			isRadio := boolValue(at(optionsBody, "isRadio"))
			f.Multiple = (kind == "checkboxesFilter" || kind == "colorFilter") && !isRadio
			many := boolValue(at(optionsBody, "hasManyValues"))
			f.OptionsTruncated = many || at(optionsBody, "openingButtons", "showAllButton") != nil
			for _, section := range arr(at(optionsBody, "sections")) {
				for _, item := range arr(at(section, "items")) {
					selected := boolValue(at(item, "isSelected"))
					f.Options = append(f.Options, FilterOption{Value: str(at(item, "key")), Label: text(at(item, "title")), Selected: selected, Image: publicURL(str(at(item, "image")))})
				}
			}
			for _, item := range arr(at(body, "colorIcons")) {
				selected := boolValue(at(item, "isSelected"))
				f.Options = append(f.Options, FilterOption{Value: str(at(item, "key")), Label: str(at(item, "description")), Selected: selected, Image: publicURL(str(at(item, "icon", "backgroundImage")))})
			}
			for _, item := range arr(at(body, "categories")) {
				selected := boolValue(at(item, "isSelected"))
				link := str(at(item, "urlValue"))
				safe, err := safePageURL(link)
				if err != nil {
					return nil, fmt.Errorf("invalid category filter URL: %w", err)
				}
				f.Options = append(f.Options, FilterOption{Value: safe, Label: text(at(item, "title")), Selected: selected, URL: WebOrigin + safe})
			}
			if kind == "boolFilter" {
				f.Options = []FilterOption{{Value: "t", Label: f.Title, Selected: f.Selected}}
			}
			if f.Key == "" || f.Title == "" {
				return nil, fmt.Errorf("Ozon returned an unsupported filter schema for key %q", f.Key)
			}
			out.Filters = append(out.Filters, f)
		}
	}
	sort, err := p.first("searchResultsSort")
	if err != nil {
		return nil, err
	}
	for _, item := range arr(at(sort, "sortButton", "options")) {
		raw := str(at(item, "action", "link"))
		safe, e := safePageURL(raw)
		if e != nil {
			continue
		}
		u, err := url.Parse(safe)
		if err != nil {
			return nil, err
		}
		selected := boolValue(at(item, "isSelected"))
		out.Sorting = append(out.Sorting, FilterOption{Value: u.Query().Get("sorting"), Label: str(at(item, "name")), Selected: selected, URL: WebOrigin + safe})
	}
	return out, nil
}

func emptySearch(p *page) (string, bool, error) {
	widgets, err := p.widgets("statusWidget")
	if err != nil {
		return "", false, err
	}
	for _, w := range widgets {
		if str(at(w, "titleAtom", "testInfo", "automatizationId")) == "not-found-title" {
			return strings.TrimSpace(text(at(w, "titleAtom")) + ". " + text(at(w, "messageAtom"))), true, nil
		}
	}
	return "", false, nil
}

func sourcePageNumber(path string) int {
	u, err := url.Parse(path)
	if err != nil {
		return 1
	}
	p, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil || p < 1 {
		return 1
	}
	return p
}
