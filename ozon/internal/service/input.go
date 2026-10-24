package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var skuPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var productPattern = regexp.MustCompile(`^/product/(?:[a-zA-Z0-9_-]*-)?([1-9][0-9]{0,19})(?:/(?:reviews|features)/?)?/?$`)

type ItemReference struct {
	SKU  string
	Path string
}

func ParseItemReference(raw string) (ItemReference, error) {
	raw = strings.TrimSpace(raw)
	if skuPattern.MatchString(raw) {
		return ItemReference{SKU: raw, Path: "/product/" + raw + "/"}, nil
	}
	if !strings.HasPrefix(raw, "https://") {
		return ItemReference{}, fmt.Errorf("invalid product %q: expected an Ozon SKU or HTTPS product URL", raw)
	}
	path, err := safePageURL(raw)
	if err != nil {
		return ItemReference{}, err
	}
	u, err := url.Parse(path)
	if err != nil {
		return ItemReference{}, err
	}
	m := productPattern.FindStringSubmatch(u.Path)
	if m == nil {
		return ItemReference{}, fmt.Errorf("invalid product URL: expected /product/<slug-or-sku>/")
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	return ItemReference{SKU: m[1], Path: "/product/" + segments[1] + "/"}, nil
}

type SearchOptions struct {
	Query          string
	Page           int
	Cursor         string
	Filters        url.Values
	IncludeFilters bool
}

type ShowOptions struct{ Full bool }

func SearchPath(o SearchOptions) (string, error) {
	if strings.TrimSpace(o.Query) == "" {
		return "", fmt.Errorf("query must not be empty")
	}
	if o.Page < 1 {
		return "", fmt.Errorf("page %d must be positive", o.Page)
	}
	if o.Cursor != "" {
		if len(o.Filters) > 0 || o.Page != 1 {
			return "", fmt.Errorf("cursor already includes page and filters; do not combine it with --page or --filter")
		}
		u, err := searchURL(o.Cursor)
		if err != nil {
			return "", err
		}
		if u.Query().Get("text") != o.Query {
			return "", fmt.Errorf("search cursor must belong to query %q", o.Query)
		}
		if recommendationSearch(u.Query()) {
			return "", fmt.Errorf("search cursor enters Ozon's recommendation feed")
		}
		if _, _, err := searchPriceRange(u.Query()); err != nil {
			return "", err
		}
		return u.String(), nil
	}
	q := url.Values{"text": {o.Query}, "from_global": {"true"}}
	for k, vs := range o.Filters {
		if !filterKeyPattern.MatchString(k) || reservedSearchKeys[k] || searchNavigationKeys[k] {
			return "", fmt.Errorf("filter key %q is not a product filter", k)
		}
		if len(vs) != 1 {
			return "", fmt.Errorf("filter %q must be specified once; combine multiple option values with semicolons", k)
		}
		for _, v := range vs {
			if v == "" {
				return "", fmt.Errorf("filter %q must have a value", k)
			}
			q.Add(k, v)
		}
	}
	if _, _, err := searchPriceRange(q); err != nil {
		return "", err
	}
	if err := normalizeSearchFilters(q); err != nil {
		return "", err
	}
	if o.Page > 1 {
		q.Set("page", strconv.Itoa(o.Page))
		q.Set("layout_page_index", strconv.Itoa(o.Page))
	}
	return "/search/?" + q.Encode(), nil
}

var filterKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var reservedSearchKeys = map[string]bool{"text": true, "url": true, "non_found": true, "sold_out_page": true, "search_tiles_req": true}

func pageNumber(value string) (int, error) {
	if value == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("page %q must be a positive integer", value)
	}
	return n, nil
}

type ReviewsOptions struct {
	Page   int
	Cursor string
	Sort   ReviewSort
	Scope  ReviewScope
}

func ReviewsPath(ref ItemReference, o ReviewsOptions) (string, error) {
	if !skuPattern.MatchString(ref.SKU) {
		return "", fmt.Errorf("invalid SKU %q", ref.SKU)
	}
	if o.Page < 1 {
		return "", fmt.Errorf("page %d must be positive", o.Page)
	}
	if _, err := NewReviewSortFromValue(o.Sort.String()); err != nil {
		return "", err
	}
	if _, err := NewReviewScopeFromValue(o.Scope.String()); err != nil {
		return "", err
	}
	if o.Cursor != "" {
		if o.Page != 1 {
			return "", fmt.Errorf("cursor already includes page; do not combine it with --page")
		}
		p, err := safePageURL(o.Cursor)
		if err != nil {
			return "", err
		}
		u, err := url.Parse(p)
		if err != nil {
			return "", err
		}
		m := productPattern.FindStringSubmatch(u.Path)
		if m == nil || m[1] != ref.SKU || !strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), "/reviews") {
			return "", fmt.Errorf("review cursor must belong to SKU %s", ref.SKU)
		}
		if _, err := pageNumber(u.Query().Get("page")); err != nil {
			return "", err
		}
		return p, nil
	}
	q := url.Values{"sort": {o.Sort.String()}, "reviewsVariantMode": {o.Scope.parameter()}}
	if o.Page > 1 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	return ref.Path + "reviews/?" + q.Encode(), nil
}
