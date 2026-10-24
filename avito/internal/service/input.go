package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const WebOrigin = "https://www.avito.ru"

var (
	numericID   = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	listingPath = regexp.MustCompile(`^/[^/]+/[^/]+/[^/]+_([1-9][0-9]{0,18})/?$`)
)

type ItemReference struct {
	ID  string
	URL string
}

func ParseItemReference(raw string) (ItemReference, error) {
	raw = strings.TrimSpace(raw)
	if numericID.MatchString(raw) {
		return ItemReference{ID: raw}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return ItemReference{}, fmt.Errorf("invalid listing URL: expected a numeric ID or an Avito listing URL without credentials")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return ItemReference{}, fmt.Errorf("invalid listing URL scheme %q; expected https", u.Scheme)
	}
	switch strings.ToLower(u.Host) {
	case "avito.ru", "www.avito.ru", "m.avito.ru":
	default:
		return ItemReference{}, fmt.Errorf("invalid listing URL host %q; expected avito.ru, www.avito.ru, or m.avito.ru", u.Host)
	}
	if strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return ItemReference{}, fmt.Errorf("invalid listing URL path")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return ItemReference{}, fmt.Errorf("invalid listing URL path: dot segments are not allowed")
		}
	}
	matches := listingPath.FindStringSubmatch(u.Path)
	if matches == nil {
		return ItemReference{}, fmt.Errorf("invalid listing URL path %q; expected /city/category/title_ID", u.Path)
	}
	canonical := &url.URL{Scheme: "https", Host: "www.avito.ru", Path: strings.TrimSuffix(u.Path, "/")}
	return ItemReference{ID: matches[1], URL: canonical.String()}, nil
}

func canonicalListingURL(raw, id string) (string, error) {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		raw = WebOrigin + raw
	}
	ref, err := ParseItemReference(raw)
	if err != nil {
		return "", err
	}
	if ref.URL == "" || ref.ID != id {
		return "", fmt.Errorf("listing URL does not match item ID %q", id)
	}
	return ref.URL, nil
}

func ValidateSearchQuery(query string, categoryID int64, page int) error {
	if (strings.TrimSpace(query) == "" && categoryID == 0) || len(query) > 2048 {
		return fmt.Errorf("query must contain 1–2048 bytes of nonblank text, or use --category-id without a query")
	}
	if categoryID < 0 {
		return fmt.Errorf("category ID %d must not be negative", categoryID)
	}
	if page < 1 {
		return fmt.Errorf("page %d must be at least 1", page)
	}
	return nil
}

func SearchURL(options SearchOptions) (*url.URL, error) {
	if err := ValidateSearchOptions(options); err != nil {
		return nil, err
	}
	if options.LocationID <= 0 {
		return nil, fmt.Errorf("location ID %d must be positive", options.LocationID)
	}
	values := url.Values{"locationId": {strconv.FormatInt(options.LocationID, 10)}}
	if options.CategoryID > 0 {
		values.Set("categoryId", strconv.FormatInt(options.CategoryID, 10))
	}
	values.Set("name", strings.TrimSpace(options.Query))
	values.Set("spaFlow", "true")
	values.Set("localPriority", "0")
	if options.LocalPriority {
		values.Set("localPriority", "1")
	}
	values.Set("disableByTitle", "false")
	values.Set("cd", "0")
	encodeFilters(values, options.Filters)
	if options.Sort.String() != "" {
		values.Set("s", options.Sort.String())
	}
	if options.Page > 1 {
		values.Set("p", strconv.Itoa(options.Page))
	}
	return &url.URL{Scheme: "https", Host: "www.avito.ru", Path: "/web/1/js/items", RawQuery: values.Encode()}, nil
}
