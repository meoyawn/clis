package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	stdhtml "html"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type webListing struct {
	ID              int64                 `json:"id"`
	Type            string                `json:"type"`
	Similar         bool                  `json:"isSimilarToSearch"`
	Title           string                `json:"title"`
	Description     string                `json:"description"`
	URLPath         string                `json:"urlPath"`
	CategoryID      int64                 `json:"categoryId"`
	LocationID      int64                 `json:"locationId"`
	Location        struct{ Name string } `json:"location"`
	AddressDetailed struct {
		LocationName string `json:"locationName"`
	} `json:"addressDetailed"`
	Price struct {
		Value      *float64 `json:"value"`
		HasValue   bool     `json:"hasValue"`
		FullString string   `json:"fullString"`
	} `json:"priceDetailed"`
	Images []map[string]string `json:"images"`
	Rating *webSellerRating    `json:"rating"`
	IVA    struct {
		UserInfoStep []webSellerInfo `json:"UserInfoStep"`
	} `json:"iva"`
}

type webSearchPayload struct {
	Count        *int64 `json:"count"`
	TotalCount   *int64 `json:"totalCount"`
	ExtraCount   *int64 `json:"extraCount"`
	ItemsOnPage  int    `json:"itemsOnPage"`
	Subscription struct {
		Authenticated *bool `json:"isAuthenticated"`
	} `json:"subscription"`
	Catalog *struct {
		Items []webListing `json:"items"`
	} `json:"catalog"`
	SearchCore webSearchCore `json:"searchCore"`
	Filters    *struct {
		Sections []struct {
			Code    string      `json:"Code"`
			Filters []webFilter `json:"Filters"`
		} `json:"Sections"`
	} `json:"filtersV2"`
	Rubricators struct {
		Categories []webCategory `json:"rubricatorCategories"`
	} `json:"rubricators"`
}

func readSearchPayload(body []byte, options SearchOptions) (*webSearchPayload, SearchContext, error) {
	var payload webSearchPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, SearchContext{}, fmt.Errorf("decode web search JSON: %w", err)
	}
	if payload.Count == nil || *payload.Count < 0 {
		return nil, SearchContext{}, fmt.Errorf("web search response has no valid count; it may be a challenge, login response, or changed schema")
	}
	selection, err := payload.SearchCore.context(options)
	if err != nil {
		return nil, SearchContext{}, err
	}
	return &payload, selection, nil
}

func ParseSearch(body []byte, options SearchOptions) (*SearchResult, error) {
	payload, selection, err := readSearchPayload(body, options)
	if err != nil {
		return nil, err
	}
	if payload.Catalog == nil || payload.Catalog.Items == nil {
		return nil, fmt.Errorf("web search response has no valid catalog.items; it may be a challenge, login response, or changed schema")
	}
	result := &SearchResult{
		SearchContext: selection, Total: *payload.Count,
		SimilarTotal: payload.ExtraCount, Page: options.Page, Results: []Listing{},
	}
	if payload.TotalCount != nil {
		// totalCount includes extraCount recommendations when exact matches run
		// out. Those are a separate catalog grid, not subsequent exact pages.
		accessible := min(*payload.TotalCount, *payload.Count)
		result.AccessibleTotal = &accessible
	}
	result.IsAuthenticated = payload.Subscription.Authenticated
	if payload.SearchCore.Query != options.Query {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Server interpreted the query as %q.", payload.SearchCore.Query))
	}
	for _, row := range payload.Catalog.Items {
		if row.Type != "item" {
			continue
		}
		if row.Similar {
			result.ExcludedCount++
			continue
		}
		if row.ID <= 0 || strings.TrimSpace(row.Title) == "" {
			return nil, fmt.Errorf("web search returned an invalid listing with ID %d", row.ID)
		}
		id := strconv.FormatInt(row.ID, 10)
		canonical, err := canonicalListingURL(row.URLPath, id)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", id, err)
		}
		amount := row.Price.Value
		if !row.Price.HasValue {
			amount = nil
		}
		if !withinPriceRange(amount, options.Filters) {
			result.ExcludedCount++
			continue
		}
		city := row.Location.Name
		if city == "" {
			city = row.AddressDetailed.LocationName
		}
		if city == "" && row.LocationID == payload.SearchCore.LocationID {
			city = payload.SearchCore.LocationName
		}
		result.Results = append(result.Results, Listing{
			ID: id, URL: canonical, Title: row.Title, Description: row.Description,
			Price:      Price{Amount: amount, Text: stdhtml.UnescapeString(row.Price.FullString)},
			CategoryID: row.CategoryID, LocationID: row.LocationID, City: city,
			Images: imageURLs(row.Images), Seller: row.seller(),
		})
	}
	result.Count = len(result.Results)
	if result.ExcludedCount > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("Excluded %d similar listings or listings without a displayed price inside the requested bounds. Server totals and pagination describe the full page.", result.ExcludedCount))
	}
	if result.AccessibleTotal != nil && payload.ItemsOnPage > 0 {
		hasMore := int64(options.Page)*int64(payload.ItemsOnPage) < *result.AccessibleTotal && result.Count+result.ExcludedCount > 0
		result.HasMore = &hasMore
		if hasMore {
			next := options.Page + 1
			result.NextPage = &next
		}
	}
	return result, nil
}

func withinPriceRange(amount *float64, filters url.Values) bool {
	for _, key := range []string{"pmin", "pmax"} {
		if raw := filters.Get(key); raw != "" {
			bound, err := strconv.ParseFloat(raw, 64)
			if err != nil || amount == nil || (key == "pmin" && *amount < bound) || (key == "pmax" && *amount > bound) {
				return false
			}
		}
	}
	return true
}

func ParseShow(body []byte, expectedID string) (*ShowResult, error) {
	page, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse listing HTML: %w", err)
	}
	var hydration []byte
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if hydration != nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "script" && node.FirstChild != nil {
			text := node.FirstChild.Data
			const marker = "window.__staticRouterHydrationData"
			if _, rest, found := strings.Cut(text, marker); found {
				rest = strings.TrimSpace(rest)
				if strings.HasPrefix(rest, "=") {
					rest = strings.TrimSpace(rest[1:])
					if strings.HasPrefix(rest, "JSON.parse(") {
						var encoded string
						if json.NewDecoder(strings.NewReader(strings.TrimPrefix(rest, "JSON.parse("))).Decode(&encoded) == nil {
							hydration = []byte(encoded)
						}
					} else {
						var raw json.RawMessage
						if json.NewDecoder(strings.NewReader(rest)).Decode(&raw) == nil {
							hydration = raw
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(page)
	if hydration == nil {
		return nil, fmt.Errorf("listing page has no supported embedded data; it may be a challenge, login page, or changed layout")
	}
	var payload struct {
		LoaderData map[string]struct {
			BuyerItem *struct {
				IsLogin *bool            `json:"isLogin"`
				Rating  *webSellerRating `json:"rating"`
				Seller  struct {
					Name string `json:"name"`
				} `json:"seller"`
				Country struct {
					Currency string `json:"currency"`
				} `json:"country"`
				Item struct {
					ID          int64    `json:"id"`
					Title       string   `json:"title"`
					URL         string   `json:"url"`
					Description string   `json:"description"`
					Price       *float64 `json:"price"`
					CategoryID  int64    `json:"categoryId"`
					LocationID  int64    `json:"locationId"`
					Location    struct {
						Name string `json:"name"`
					} `json:"location"`
					Images         []map[string]string `json:"imageUrls"`
					IsActive       *bool               `json:"isActive"`
					FormattedPrice struct {
						Text     string `json:"formatedString"`
						HasValue bool   `json:"isHasValue"`
					} `json:"formattedPrice"`
				} `json:"item"`
				Params struct {
					Items []struct {
						Title       string `json:"title"`
						Description string `json:"description"`
					} `json:"items"`
				} `json:"paramsBlock"`
			} `json:"buyerItem"`
		} `json:"loaderData"`
	}
	if err := json.Unmarshal(hydration, &payload); err != nil {
		return nil, fmt.Errorf("decode listing data: %w", err)
	}
	for _, route := range payload.LoaderData {
		if route.BuyerItem == nil {
			continue
		}
		buyer := route.BuyerItem
		item := buyer.Item
		id := strconv.FormatInt(item.ID, 10)
		if id != expectedID || item.Title == "" {
			return nil, fmt.Errorf("requested listing %s but page contains listing %s or no title", expectedID, id)
		}
		canonical, err := canonicalListingURL(item.URL, id)
		if err != nil {
			return nil, err
		}
		amount := item.Price
		if !item.FormattedPrice.HasValue {
			amount = nil
		}
		result := &ShowResult{Listing: Listing{
			ID: id, URL: canonical, Title: item.Title, Description: plainText(item.Description),
			Price:      Price{Amount: amount, Text: stdhtml.UnescapeString(item.FormattedPrice.Text), Currency: currencyCode(buyer.Country.Currency)},
			CategoryID: item.CategoryID, LocationID: item.LocationID, City: item.Location.Name,
			Images: imageURLs(item.Images), Active: item.IsActive, Seller: buyer.Rating.seller(buyer.Seller.Name),
		}}
		result.IsAuthenticated = buyer.IsLogin
		for _, param := range buyer.Params.Items {
			result.Parameters = append(result.Parameters, Parameter{Name: plainText(param.Title), Value: plainText(param.Description)})
		}
		return result, nil
	}
	return nil, fmt.Errorf("listing page has no buyerItem.item data for ID %s", expectedID)
}

func plainText(markup string) string {
	nodes, err := html.ParseFragment(strings.NewReader(markup), nil)
	if err != nil {
		return strings.TrimSpace(stdhtml.UnescapeString(markup))
	}
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		block := n.Type == html.ElementNode && (n.Data == "p" || n.Data == "div" || n.Data == "br" || n.Data == "li")
		if block {
			text.WriteByte('\n')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if block {
			text.WriteByte('\n')
		}
	}
	for _, node := range nodes {
		walk(node)
	}
	var lines []string
	for _, line := range strings.Split(text.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n\n")
}

func currencyCode(value string) string {
	switch value {
	case "руб.", "руб", "₽", "RUB", "RUR":
		return "RUB"
	default:
		return value
	}
}

func imageURLs(images []map[string]string) []string {
	var result []string
	for _, sizes := range images {
		best, area := "", 0
		for size, raw := range sizes {
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.User != nil || !strings.HasSuffix(u.Hostname(), ".avito.st") {
				continue
			}
			var w, h int
			if _, err := fmt.Sscanf(size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 || w > 20000 || h > 20000 {
				continue
			}
			if w*h > area || (w*h == area && raw < best) {
				best, area = raw, w*h
			}
		}
		if best != "" {
			result = append(result, best)
		}
	}
	return result
}
