package service

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// These parameters may change while redirecting or paging. Other parameters
// are constraints, except for a newly predicted brand adopted on a redirect.
var searchNavigationKeys = map[string]bool{
	"page": true, "layout_page_index": true, "layout_container": true,
	"paginator_token": true, "search_page_state": true, "start_page_id": true,
	"from_global": true, "page_changed": true, "deny_category_prediction": true,
	"brand_was_predicted": true, "category_was_predicted": true,
	"__rr": true,
}

var categorySearchPath = regexp.MustCompile(`^/category/(?:[a-zA-Z0-9_-]+/)+$`)

func searchURL(raw string) (*url.URL, error) {
	clean, err := safePageURL(raw)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(clean)
	if err != nil {
		return nil, err
	}
	if u.Path != "/search/" && !categorySearchPath.MatchString(u.Path) {
		return nil, fmt.Errorf("expected an Ozon search or category page")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid search parameters: %w", err)
	}
	// Ozon echoes this request-local API flag into page URLs and cursors.
	// Replaying it inside the next page URL duplicates it on the next redirect.
	if q.Has("__rr") {
		q.Del("__rr")
		u.RawQuery = q.Encode()
	}
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return nil, fmt.Errorf("search parameter %q must have exactly one nonempty value", key)
		}
	}
	if strings.TrimSpace(q.Get("text")) == "" {
		return nil, fmt.Errorf("search URL has no query")
	}
	n, err := pageNumber(q.Get("page"))
	if err != nil {
		return nil, err
	}
	if q.Has("layout_page_index") {
		index, err := pageNumber(q.Get("layout_page_index"))
		if err != nil || index != n {
			return nil, fmt.Errorf("search page and layout page disagree")
		}
	}
	return u, nil
}

func searchRedirectURL(raw string) (*url.URL, *url.URL, error) {
	return pageRedirectURL(raw, searchURL)
}

// Keep API navigation parameters outside the nested page URL. Each caller
// validates the page against the scope of its own operation.
func pageRedirectURL(raw string, parsePage func(string) (*url.URL, error)) (*url.URL, *url.URL, error) {
	clean, err := safePageURL(raw)
	if err != nil {
		return nil, nil, err
	}
	u, err := url.Parse(clean)
	if err != nil {
		return nil, nil, err
	}
	if u.Path == entrypointPath {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(q["url"]) != 1 {
			return nil, nil, fmt.Errorf("redirect must identify exactly one Ozon page")
		}
		// These API parameters accompany the nested page URL. Other keys
		// could override its validated query, filters or requested page.
		for key, values := range q {
			if key != "url" && key != "__rr" && key != "page_changed" {
				return nil, nil, fmt.Errorf("unsupported Ozon API redirect parameter %q", key)
			}
			if len(values) != 1 || values[0] == "" {
				return nil, nil, fmt.Errorf("Ozon API redirect parameter %q must have exactly one nonempty value", key)
			}
		}
		next, err := parsePage(q.Get("url"))
		if err != nil {
			return nil, nil, err
		}
		target := entrypointURL(next.String())
		q.Set("url", next.String())
		target.RawQuery = q.Encode()
		return next, target, nil
	}
	next, err := parsePage(clean)
	if err != nil {
		return nil, nil, err
	}
	return next, entrypointURL(next.String()), nil
}

func searchRequestKey(pageURL, target *url.URL) string {
	q := target.Query()
	q.Set("url", pageURL.Path+"?"+pageURL.Query().Encode())
	return target.Path + "?" + q.Encode()
}

func sameSearchScope(want, got *url.URL) error {
	if want.Path != got.Path {
		return fmt.Errorf("Ozon changed the search category")
	}
	return sameSearchConstraints(want.Query(), got.Query())
}

func recommendationSearch(q url.Values) bool {
	// Ozon uses these to continue a search in its zero-result/sold-out feed.
	return q.Has("non_found") || q.Has("sold_out_page") || q.Has("search_tiles_req")
}

// Ozon can infer a brand from the query on a redirect. Adopt only an explicitly
// marked prediction when no brand was already set. Subsequent redirects,
// responses and pagination must preserve the adopted brand like any filter.
func sameSearchRedirectConstraints(want, got url.Values) error {
	if !want.Has("brand") && skuPattern.MatchString(got.Get("brand")) && canonicalFilterValue(got.Get("brand_was_predicted")) == "t" {
		adopted := make(url.Values, len(want)+1)
		for key, values := range want {
			adopted[key] = values
		}
		adopted.Set("brand", got.Get("brand"))
		return sameSearchConstraints(adopted, got)
	}
	return sameSearchConstraints(want, got)
}

func sameSearchConstraints(want, got url.Values) error {
	keys := make([]string, 0, len(want)+len(got))
	for k := range want {
		keys = append(keys, k)
	}
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range slices.Compact(keys) {
		if searchNavigationKeys[k] {
			continue
		}
		a, b := want.Get(k), got.Get(k)
		if k != "text" {
			a, b = canonicalFilterValue(a), canonicalFilterValue(b)
		}
		if a != b {
			return fmt.Errorf("Ozon changed search constraint %q", k)
		}
	}
	return nil
}

var decimalFilterValue = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)

func canonicalFilterValue(raw string) string {
	if raw == "true" {
		return "t"
	}
	if raw == "false" {
		return "f"
	}
	parts := strings.Split(raw, ";")
	for i, value := range parts {
		if !decimalFilterValue.MatchString(value) {
			continue
		}
		whole, fraction, _ := strings.Cut(value, ".")
		whole = strings.TrimLeft(whole, "0")
		if whole == "" {
			whole = "0"
		}
		fraction = strings.TrimRight(fraction, "0")
		parts[i] = whole
		if fraction != "" {
			parts[i] += "." + fraction
		}
	}
	return strings.Join(parts, ";")
}

// Sending the canonical wire representation avoids Ozon's HTTP redirects.
func normalizeSearchFilters(q url.Values) error {
	for key := range q {
		if key == "text" || searchNavigationKeys[key] {
			continue
		}
		value := q.Get(key)
		if value == "true" || value == "false" {
			q.Set(key, canonicalFilterValue(value))
		}
	}
	if q.Has("currency_price") {
		parts := strings.Split(canonicalFilterValue(q.Get("currency_price")), ";")
		for i, part := range parts {
			whole, fraction, _ := strings.Cut(part, ".")
			if len(fraction) > 3 {
				return fmt.Errorf("currency_price supports at most three decimal places")
			}
			parts[i] = whole + "." + fraction + strings.Repeat("0", 3-len(fraction))
		}
		q.Set("currency_price", strings.Join(parts, ";"))
	}
	return nil
}

func validateSearchResponse(p *page, path string) (*url.URL, error) {
	want, err := searchURL(path)
	if err != nil {
		return nil, err
	}
	got, err := searchURL(p.PageInfo.URL)
	pageType := "search"
	if categorySearchPath.MatchString(want.Path) {
		pageType = "category"
	}
	if err != nil || p.PageInfo.PageType != pageType {
		return nil, fmt.Errorf("Ozon response does not identify the requested search page")
	}
	if recommendationSearch(want.Query()) || recommendationSearch(got.Query()) {
		return nil, fmt.Errorf("Ozon returned a recommendation feed instead of search matches")
	}
	if err := sameSearchScope(want, got); err != nil {
		return nil, err
	}
	if sourcePageNumber(want.String()) != sourcePageNumber(got.String()) {
		return nil, fmt.Errorf("Ozon returned a different search page")
	}
	tags, err := p.first("tapTags")
	if err != nil {
		return nil, err
	}
	if actual := str(tags["searchText"]); actual != "" && actual != want.Query().Get("text") {
		return nil, fmt.Errorf("Ozon rewrote the search query")
	}
	return want, nil
}

// A URL alone is not proof that Ozon applied a filter: it echoes unknown keys.
// Full search pages expose the selected facets; incremental pages preserve the
// previously checked constraints in their continuation and pageInfo URLs.
func validateActiveFilters(p *page, q url.Values) error {
	if q.Has("sorting") {
		sort, err := p.first("searchResultsSort")
		if err != nil {
			return err
		}
		if sort != nil || !q.Has("paginator_token") {
			confirmed := false
			for _, option := range arr(at(sort, "sortButton", "options")) {
				if selected := boolValue(at(option, "isSelected")); !selected {
					continue
				}
				u, err := searchURL(str(at(option, "action", "link")))
				if err == nil && u.Query().Get("sorting") == q.Get("sorting") {
					confirmed = true
				}
			}
			if !confirmed {
				return fmt.Errorf("Ozon did not confirm the requested sorting; use a sorting value from ozon filters")
			}
		}
	}
	w, err := p.first("searchResultsFiltersActive")
	if err != nil {
		return err
	}
	if w == nil {
		if !q.Has("paginator_token") {
			for k := range q {
				if k != "text" && k != "sorting" && !searchNavigationKeys[k] {
					return fmt.Errorf("Ozon did not confirm search filter %q", k)
				}
			}
		}
		return nil
	}
	active := map[string][]string{}
	for _, f := range arr(w["activeFilters"]) {
		key := str(at(f, "key"))
		for _, value := range arr(at(f, "activeValues")) {
			v := str(at(value, "cellTrackingInfo", "filterValue"))
			if str(at(f, "ftype")) == "RESPONSE_FILTER_TYPE_RANGE" {
				v = strings.ReplaceAll(v, ":", ";")
			}
			active[key] = append(active[key], canonicalFilterValue(v))
		}
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if key == "text" || key == "sorting" || searchNavigationKeys[key] {
			continue
		}
		values := active[key]
		expected := []string{canonicalFilterValue(q.Get(key))}
		if len(values) > 1 {
			expected = strings.Split(expected[0], ";")
		}
		slices.Sort(values)
		slices.Sort(expected)
		if !slices.Equal(values, expected) {
			return fmt.Errorf("Ozon did not confirm the requested value for filter %q; use values from ozon filters", key)
		}
	}
	return nil
}

// Select by the layout's producer, not by the visual tileGridDesktop component:
// recommendation carousels may render that same component with unrelated SKUs.
func searchGrid(p *page) (object, string, error) {
	var gridID, paginatorID string
	var count int
	var walk func([]layoutWidget, string)
	walk = func(nodes []layoutWidget, paginator string) {
		for _, node := range nodes {
			parent := paginator
			if node.Component == "infiniteVirtualPaginator" && node.Name == "composer.paginator" {
				parent = node.StateID
			}
			if node.Component == "tileGridDesktop" && node.Name == "catalog.searchResultsV2" {
				gridID, paginatorID = node.StateID, parent
				count++
			}
			for _, slot := range node.Placeholders {
				walk(slot.Widgets, parent)
			}
		}
	}
	walk(p.Layout, "")
	if count != 1 || gridID == "" {
		return nil, "", fmt.Errorf("Ozon response does not identify exactly one search result grid; refusing recommendation or unknown grids")
	}
	grid, err := p.widget(gridID)
	if err != nil {
		return nil, "", err
	}
	next := p.NextPage
	if next == "" && paginatorID != "" {
		paginator, err := p.widget(paginatorID)
		if err != nil {
			return nil, "", err
		}
		next = str(paginator["nextPage"])
	}
	return grid, next, nil
}

func searchPriceRange(q url.Values) (*float64, *float64, error) {
	if !q.Has("currency_price") {
		return nil, nil, nil
	}
	parts := strings.Split(q.Get("currency_price"), ";")
	if len(parts) == 2 && decimalFilterValue.MatchString(parts[0]) && decimalFilterValue.MatchString(parts[1]) {
		low, e1 := strconv.ParseFloat(parts[0], 64)
		high, e2 := strconv.ParseFloat(parts[1], 64)
		if e1 == nil && e2 == nil && low <= high {
			return &low, &high, nil
		}
	}
	return nil, nil, fmt.Errorf("currency_price must be a nonnegative MIN;MAX range with MIN <= MAX")
}
