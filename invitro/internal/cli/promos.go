package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const maxPromoArticles = 20

type promoLink struct {
	BlockType string `json:"blockType"`
	NewPage   struct {
		Slug string `json:"slug"`
	} `json:"newPage"`
	OldPageURL string `json:"oldPageUrl"`
}

type apiPromotion struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	StartDate   *time.Time  `json:"startDate"`
	EndDate     *time.Time  `json:"endDate"`
	Enabled     bool        `json:"enabled"`
	Status      string      `json:"_status"`
	Platform    []string    `json:"platform"`
	Page        []promoLink `json:"page"`
}

type promotion struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Slug        string     `json:"slug,omitempty"`
	URL         string     `json:"url"`
	StartsAt    *time.Time `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
}

type promotionsResult struct {
	City         string          `json:"city"`
	CityID       string          `json:"city_id"`
	Count        int             `json:"count"`
	APITotal     int             `json:"api_total"`
	Page         int             `json:"page"`
	TotalPages   int             `json:"total_pages"`
	HasMore      bool            `json:"has_more"`
	NextPage     *int            `json:"next_page"`
	Results      []promotion     `json:"results"`
	SourceURL    string          `json:"source_url"`
	FetchedAt    time.Time       `json:"fetched_at"`
	HTTPRequests int             `json:"http_requests"`
	Source       json.RawMessage `json:"source,omitempty"`
}

type promotionSection struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text"`
}

type promotionOffer struct {
	product
	Article        string          `json:"article"`
	PromotionTitle string          `json:"promotion_title"`
	Source         json.RawMessage `json:"source,omitempty"`
}

type promotionDetail struct {
	promotion
	City         string             `json:"city"`
	CityID       string             `json:"city_id"`
	Sections     []promotionSection `json:"sections"`
	Links        []string           `json:"links"`
	Offers       []promotionOffer   `json:"offers"`
	PricingNote  string             `json:"pricing_note"`
	SourceURL    string             `json:"source_url"`
	FetchedAt    time.Time          `json:"fetched_at"`
	HTTPRequests int                `json:"http_requests"`
	Source       json.RawMessage    `json:"source,omitempty"`
}

func promotionsURL(city string) string {
	if city == "moscow" {
		return siteOrigin + "/ak/"
	}
	return siteOrigin + "/" + city + "/ak/"
}

func parsePromotionReference(value string) (slug, city string, err error) {
	if len(value) <= 200 && citySlug.MatchString(value) {
		return value, "", nil
	}
	u, e := url.Parse(value)
	if e == nil && u.Scheme == "https" && (u.Host == "www.invitro.ru" || u.Host == "invitro.ru") && u.User == nil && u.RawPath == "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		city = "moscow"
		if len(parts) == 3 {
			city, parts = parts[0], parts[1:]
		}
		if len(parts) == 2 && parts[0] == "ak" && citySlug.MatchString(city) && len(parts[1]) <= 200 && citySlug.MatchString(parts[1]) {
			return parts[1], city, nil
		}
	}
	return "", "", fmt.Errorf("invalid promotion reference %.200q: expected a slug or HTTPS Invitro /[city/]ak/<slug>/ URL", value)
}

func (c *client) Promotions(ctx context.Context, city cityInfo, page int, full bool, now time.Time) (*promotionsResult, error) {
	if page < 1 || page > maxPage {
		return nil, fmt.Errorf("promotion page %d must be between 1 and %d", page, maxPage)
	}
	var data json.RawMessage
	q := url.Values{"targetPage": {"promotions"}, "cityId": {city.ID}, "depth": {"3"}, "page": {strconv.Itoa(page)}}
	if err := c.request(ctx, "/golk/cms/cms-proxy/promotions/filtered", q, nil, &data); err != nil {
		return nil, err
	}
	var raw struct {
		Docs       []apiPromotion `json:"docs"`
		Total      *int           `json:"totalDocs"`
		Page       int            `json:"page"`
		TotalPages int            `json:"totalPages"`
		HasNext    bool           `json:"hasNextPage"`
		NextPage   *int           `json:"nextPage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode promotions: %w", err)
	}
	if raw.Docs == nil || raw.Total == nil || *raw.Total < 0 || raw.Page != page || raw.TotalPages < 1 || page > raw.TotalPages || raw.HasNext != (page < raw.TotalPages) || (raw.HasNext && (raw.NextPage == nil || *raw.NextPage != page+1)) {
		return nil, fmt.Errorf("invalid promotion pagination for page %d", page)
	}
	result := &promotionsResult{City: city.Slug, CityID: city.ID, Page: page, APITotal: *raw.Total, TotalPages: raw.TotalPages, HasMore: raw.HasNext, NextPage: raw.NextPage, Results: []promotion{}, SourceURL: promotionsURL(city.Slug), FetchedAt: now, HTTPRequests: c.requests}
	for _, p := range raw.Docs {
		if !p.Enabled || p.Status != "published" || (len(p.Platform) > 0 && !slices.Contains(p.Platform, "web")) || (p.StartDate != nil && now.Before(*p.StartDate)) || (p.EndDate != nil && !now.Before(*p.EndDate)) {
			continue
		}
		if p.ID == "" || strings.TrimSpace(p.Title) == "" {
			return nil, fmt.Errorf("invalid promotion %q: missing ID or title", p.ID)
		}
		item := promotion{ID: p.ID, Title: plainText(p.Title), Description: plainText(p.Description), StartsAt: p.StartDate, EndsAt: p.EndDate}
		for _, link := range p.Page {
			switch link.BlockType {
			case "newPageLink":
				if !citySlug.MatchString(link.NewPage.Slug) {
					return nil, fmt.Errorf("invalid promotion slug %q", link.NewPage.Slug)
				}
				item.Slug, item.URL = link.NewPage.Slug, promotionsURL(city.Slug)+link.NewPage.Slug+"/"
			case "oldPageLink":
				item.URL = strings.ReplaceAll(link.OldPageURL, "/city/", "/"+city.Slug+"/")
			}
			if item.URL != "" {
				break
			}
		}
		result.Results = append(result.Results, item)
	}
	result.Count = len(result.Results)
	if full {
		result.Source = data
	}
	return result, nil
}

// Traverse display fields only: image metadata, geography, IDs and style fields
// are not prose. Lexical children retain their paragraph boundaries.
func cmsText(value any) string {
	switch v := value.(type) {
	case string:
		return plainText(v)
	case []any:
		parts := []string{}
		for _, item := range v {
			if s := cmsText(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if v["type"] == "linebreak" {
			return "\n"
		}
		if text, ok := v["text"].(string); ok {
			return text
		}
		if children, ok := v["children"].([]any); ok {
			parts := []string{}
			for _, child := range children {
				parts = append(parts, cmsText(child))
			}
			separator := ""
			if v["type"] == "root" || v["type"] == "list" {
				separator = "\n"
			}
			return strings.Join(parts, separator)
		}
		parts := []string{}
		for _, key := range []string{"root", "content", "title", "label", "caption", "description", "textBlockContent", "textBlockDescription", "cards", "list"} {
			// The redesigned CMS keeps legacy copy after migrating a section.
			// The website renders the new rich content when it is present.
			if key == "textBlockDescription" {
				if content, ok := v["textBlockContent"].([]any); ok && len(content) > 0 {
					continue
				}
			}
			if s := cmsText(v[key]); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (c *client) Promotion(ctx context.Context, city cityInfo, p promotion, full bool) (*promotionDetail, error) {
	c.requestLimit = 3 + maxPromoArticles
	var data json.RawMessage
	if err := c.request(ctx, "/golk/cms/cms-proxy/pages/filtered", url.Values{"slug": {p.Slug}, "cityId": {city.ID}, "depth": {"2"}}, nil, &data); err != nil {
		return nil, err
	}
	var raw struct {
		Slug     string           `json:"slug"`
		Sections []map[string]any `json:"sections"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode promotion %q: %w", p.Slug, err)
	}
	if raw.Slug != p.Slug || raw.Sections == nil {
		return nil, fmt.Errorf("invalid promotion page for slug %q", p.Slug)
	}
	result := &promotionDetail{promotion: p, City: city.Slug, CityID: city.ID, Sections: []promotionSection{}, Links: []string{}, Offers: []promotionOffer{}, SourceURL: p.URL,
		PricingNote: "Live prices for the promotion's own articles, not an automatic discount on regular catalog products. Check linked rules for eligibility, collection fees and compatibility with bonuses; an empty additional_services list does not establish that collection is free."}
	seen := map[string]bool{}
	for _, s := range raw.Sections {
		title, _ := s["sectionTitle"].(string)
		if title == "" {
			title, _ = s["textBlockTitle"].(string)
		}
		if text := strings.TrimSpace(cmsText(s)); text != "" || title != "" {
			name, _ := s["sectionName"].(string)
			blockType, _ := s["blockType"].(string)
			result.Sections = append(result.Sections, promotionSection{Name: name, Type: blockType, Title: plainText(title), Text: text})
		}
		if actions, ok := s["action"].([]any); ok {
			for _, a := range actions {
				if m, ok := a.(map[string]any); ok {
					if link, ok := m["link"].(string); ok && link != "" {
						link = strings.ReplaceAll(link, "/city/", "/"+city.Slug+"/")
						base, _ := url.Parse(p.URL)
						if target, err := url.Parse(link); err == nil {
							resolved := base.ResolveReference(target).String()
							if !slices.Contains(result.Links, resolved) {
								result.Links = append(result.Links, resolved)
							}
						}
					}
				}
			}
		}
		cards, _ := s["cards"].([]any)
		for _, card := range cards {
			m, ok := card.(map[string]any)
			if !ok {
				continue
			}
			article, _ := m["article"].(string)
			if article == "" {
				continue
			}
			typeValue, _ := m["promotionType"].(string)
			kind, err := NewProductKindFromValue(strings.ToLower(typeValue))
			if err != nil {
				return nil, fmt.Errorf("promotion article %q: %w", article, err)
			}
			key := kind.value + ":" + article
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(seen) > maxPromoArticles {
				return nil, fmt.Errorf("promotion %q exceeds the limit of %d articles", p.Slug, maxPromoArticles)
			}
			title, _ := m["title"].(string)
			result.Offers = append(result.Offers, promotionOffer{Article: article, product: product{Type: kind.value}, PromotionTitle: plainText(title)})
		}
	}
	for i := range result.Offers {
		offer := &result.Offers[i]
		kind, _ := NewProductKindFromValue(offer.Type)
		var source json.RawMessage
		if err := c.request(ctx, "/golk/tests/api/v1/"+kind.apiPath()+"/by-article", url.Values{"article": {offer.Article}, "cityID": {city.ID}}, nil, &source); err != nil {
			return nil, err
		}
		var product apiProduct
		if err := json.Unmarshal(source, &product); err != nil {
			return nil, fmt.Errorf("decode promotion article %q: %w", offer.Article, err)
		}
		if !uuidPattern.MatchString(product.ID) || product.Title == "" || (product.Code != offer.Article && product.Article != offer.Article) || (product.Price != nil && *product.Price < 0) {
			return nil, fmt.Errorf("invalid or mismatched promotion article %q", offer.Article)
		}
		offer.product = productFacts(product, kind)
		offer.URL = p.URL
		if full {
			offer.Source = source
		}
	}
	result.HTTPRequests, result.FetchedAt = c.requests, time.Now().UTC()
	if full {
		result.Source = data
	}
	return result, nil
}

func runPromos(ctx context.Context, args []string, stdout, stderr io.Writer, locate func() (appPaths, error), origin string) error {
	flags := flag.NewFlagSet("promos", flag.ContinueOnError)
	flags.SetOutput(stderr)
	city := flags.String("city", "", "city URL slug")
	page := flags.Int("page", 1, "one page of website promotions")
	full := flags.Bool("full", false, "include source JSON")
	timeout := flags.Duration("timeout", 90*time.Second, "timeout for the entire command")
	ua := flags.String("user-agent", defaultUserAgent, "HTTP User-Agent")
	if err := parseOptions(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("promos expects at most one slug or URL, got %d arguments", flags.NArg())
	}
	if *page < 1 || *page > maxPage {
		return fmt.Errorf("page %d must be between 1 and %d", *page, maxPage)
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %q must be positive", timeout.String())
	}
	slug := ""
	if flags.NArg() == 1 {
		var urlCity string
		var err error
		slug, urlCity, err = parsePromotionReference(flags.Arg(0))
		if err != nil {
			return err
		}
		if !flagSet(flags, "city") {
			*city = urlCity
		}
	}
	paths, err := locate()
	if err != nil {
		return err
	}
	config, err := loadConfig(paths.config)
	if err != nil {
		return err
	}
	if !flagSet(flags, "city") && *city == "" {
		*city = config.City
	}
	if err := validateCity(*city); err != nil {
		return err
	}
	c, err := newClient(origin, *ua)
	if err != nil {
		return err
	}
	defer c.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	location, err := c.resolveCity(ctx, *city)
	if err != nil {
		return err
	}
	list, err := c.Promotions(ctx, location, *page, *full, time.Now().UTC())
	if err != nil {
		return err
	}
	if slug == "" {
		return writeJSON(stdout, list)
	}
	for _, p := range list.Results {
		if p.Slug != slug {
			continue
		}
		detail, err := c.Promotion(ctx, location, p, *full)
		if err != nil {
			return err
		}
		return writeJSON(stdout, detail)
	}
	return fmt.Errorf("promotion %q is not active on page %d for city %q (available pages: %d); run promos --city %s to inspect offers", slug, *page, *city, list.TotalPages, *city)
}
