package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/meoyawn/clis/dns-shop/internal/domain"
)

type Options struct {
	Page           int
	Limit          int
	IncludeFilters bool
	Category       string
	Sort           domain.Sort
	Filters        url.Values
}

func (o Options) Validate() error {
	if o.Page < 1 || o.Limit < 0 {
		return fmt.Errorf("page must be positive and limit must not be negative")
	}
	if o.Sort.String() != "" {
		if _, err := domain.NewSort(o.Sort.String()); err != nil {
			return err
		}
	}
	for key := range o.Filters {
		if key == "q" || key == "p" || key == "order" || key == "category" {
			return fmt.Errorf("filter %q is reserved; use the dedicated flag", key)
		}
	}
	return nil
}

// Search loads one search page and its prices. The storefront redirects a
// query without a category to its best-matching category.
func (c *Client) Search(ctx context.Context, query string, o Options) (*domain.Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query must not be empty")
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	q := url.Values{"q": {query}}
	if o.Category != "" {
		q.Set("category", o.Category)
	}
	if o.Sort.String() != "" && o.Sort.String() != "popular" {
		q.Set("order", o.Sort.String())
	}
	if o.Page > 1 {
		q.Set("p", strconv.Itoa(o.Page))
	}
	for key, values := range o.Filters {
		q.Set(key, strings.Join(values, "-"))
	}
	resp, err := c.fetch(ctx, request{method: http.MethodGet, target: c.origin + "/search/?" + q.Encode()})
	if err != nil {
		return nil, err
	}
	// The redirect to the best-matching category drops sort, page and filter
	// parameters; re-apply them to the category URL once.
	if final := resp.url.Query(); resp.url.Path == "/search/" && final.Get("category") != "" && o.Category == "" {
		lost := false
		for key := range q {
			if final.Get(key) == "" {
				final.Set(key, q.Get(key))
				lost = true
			}
		}
		if lost {
			if resp, err = c.fetch(ctx, request{method: http.MethodGet, target: c.origin + "/search/?" + final.Encode()}); err != nil {
				return nil, err
			}
		}
	}
	if strings.HasPrefix(resp.url.Path, "/product/") {
		return nil, fmt.Errorf("the query opened a single product page %s; use details", resp.url.String())
	}
	page, err := parseSearch(resp.body, c.origin)
	if err != nil {
		return nil, err
	}
	result := &domain.Result{URL: resp.url.String(), City: SessionCity(c.session).Name, Category: page.category, Total: page.total, Page: o.Page, HasMore: page.hasMore, Products: page.products}
	if len(page.products) > 0 {
		if err = c.prices(ctx, resp.url.String(), page); err != nil {
			return nil, err
		}
	}
	if o.IncludeFilters {
		target := c.origin + "/catalog/search/filters/?" + withoutPage(resp.url.Query()).Encode()
		f, err := c.fetch(ctx, request{method: http.MethodGet, target: target, xhr: true, csrf: page.csrf, referer: resp.url.String()})
		if err != nil {
			return nil, err
		}
		if result.Filters, err = parseFilters(f.body); err != nil {
			return nil, err
		}
	}
	result.PageSize = len(result.Products)
	if o.Limit > 0 && len(result.Products) > o.Limit {
		result.Products = result.Products[:o.Limit]
	}
	result.HTTPRequests = c.requests
	return result, nil
}

func withoutPage(q url.Values) url.Values {
	q.Del("p")
	return q
}

type searchPage struct {
	products []domain.Product
	category string
	total    *int
	hasMore  bool
	csrf     string
	buyHash  string
	// buyContainers maps ajax-state container IDs to product codes.
	buyContainers []container
}

type container struct {
	ID   string `json:"id"`
	Data struct {
		ID string `json:"id"`
	} `json:"data"`
}

var (
	digits      = regexp.MustCompile(`\d+`)
	categoryRe  = regexp.MustCompile(`в категории\s+(.+)$`)
	buyStateRe  = regexp.MustCompile(`\{"type":"product-buy","hash":"([0-9a-f]+)"[^{}]*\},`)
	productGUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

func parseSearch(body []byte, origin string) (*searchPage, error) {
	doc, err := document(body)
	if err != nil {
		return nil, err
	}
	page := &searchPage{products: []domain.Product{}, csrf: csrfToken(doc)}
	cards := doc.Find(`div[data-id="product"]`)
	empty := doc.Find(".empty-search-results, .products-page__empty").Length() > 0
	if cards.Length() == 0 && !empty && doc.Find(`[data-role="items-count"]`).Length() == 0 {
		return nil, fmt.Errorf("search page has no product list; the storefront markup may have changed")
	}
	if n, err := strconv.Atoi(strings.TrimSpace(doc.Find(".products-filtered-count").First().Text())); err == nil {
		page.total = &n
	}
	title := strings.Join(strings.Fields(doc.Find(`[data-role="items-count"]`).First().Text()), " ")
	if m := categoryRe.FindStringSubmatch(title); m != nil {
		page.category = m[1]
	}
	page.hasMore = doc.Find(`[data-role="show-more-btn"]`).Length() > 0
	cards.Each(func(_ int, s *goquery.Selection) {
		page.products = append(page.products, card(s, origin))
	})
	if m := buyStateRe.FindSubmatchIndex(body); m != nil {
		page.buyHash = string(body[m[2]:m[3]])
		if err := json.NewDecoder(strings.NewReader(string(body[m[1]:]))).Decode(&page.buyContainers); err != nil {
			return nil, fmt.Errorf("decode price containers: %w", err)
		}
	}
	return page, nil
}

func card(s *goquery.Selection, origin string) domain.Product {
	p := domain.Product{ID: s.AttrOr("data-product", ""), Code: s.AttrOr("data-code", ""), Availability: s.AttrOr("data-avail-status", ""), Images: []string{}, Characteristics: []domain.Characteristic{}}
	name := s.Find("a.catalog-product__name").First()
	p.URL = absolute(origin, name.AttrOr("href", ""))
	name.Find(".catalog-product__short-specs").Remove()
	p.Title = strings.Join(strings.Fields(name.Text()), " ")
	img := s.Find(".catalog-product__image picture").First()
	if src := img.Find(`source[media="(min-width: 768px)"]`).AttrOr("data-srcset", ""); src != "" {
		p.Images = append(p.Images, src)
	} else if src := img.Find("img").AttrOr("data-src", ""); src != "" {
		p.Images = append(p.Images, src)
	}
	rating := s.Find("a.catalog-product__rating").First()
	if v, err := strconv.ParseFloat(strings.TrimSpace(rating.Find("b").Text()), 64); err == nil {
		p.Rating = &v
	}
	if rating.Length() > 0 {
		rating.Find("b").Remove()
		if m := digits.FindString(strings.ReplaceAll(rating.Text(), " ", "")); m != "" {
			n, _ := strconv.Atoi(m)
			p.ReviewCount = &n
		}
	}
	p.Reliability = strings.TrimSpace(s.Find(".catalog-product__service-rating").First().Text())
	s.Find(".catalog-product__spec").Each(func(_ int, spec *goquery.Selection) {
		name := strings.TrimSuffix(strings.TrimSpace(spec.Find(".catalog-product__spec-label").Text()), ":")
		value := strings.Join(strings.Fields(spec.Find(".catalog-product__spec-value").Text()), " ")
		if name != "" && value != "" {
			p.Characteristics = append(p.Characteristics, domain.Characteristic{Name: name, Value: value})
		}
	})
	return p
}

func absolute(origin, href string) string {
	base, _ := url.Parse(origin)
	u, err := url.Parse(href)
	if err != nil || href == "" {
		return ""
	}
	return base.ResolveReference(u).String()
}

func csrfToken(doc *goquery.Document) string {
	return doc.Find(`meta[name="csrf-token"]`).AttrOr("content", "")
}

type priceState struct {
	ID    string `json:"id"`
	Price *struct {
		Current  *float64 `json:"current"`
		Previous *float64 `json:"previous"`
	} `json:"price"`
}

// prices replays the storefront's product-buy state request, which carries the
// current and crossed-out prices that the server-rendered cards omit.
func (c *Client) prices(ctx context.Context, referer string, page *searchPage) error {
	if page.buyHash == "" || len(page.buyContainers) == 0 || page.csrf == "" {
		return fmt.Errorf("search page lacks the price request configuration")
	}
	payload := map[string]any{"type": "product-buy", "hash": page.buyHash, "containers": page.buyContainers}
	data, _ := json.Marshal(payload)
	resp, err := c.fetch(ctx, request{method: http.MethodPost, target: c.origin + "/ajax-state/product-buy/", form: url.Values{"data": {string(data)}}, xhr: true, csrf: page.csrf, referer: referer})
	if err != nil {
		return err
	}
	var out struct {
		Result bool `json:"result"`
		Data   struct {
			States []struct {
				ID   string     `json:"id"`
				Data priceState `json:"data"`
			} `json:"states"`
		} `json:"data"`
	}
	if err = json.Unmarshal(resp.body, &out); err != nil || !out.Result {
		return fmt.Errorf("decode prices: unexpected response")
	}
	codes := map[string]string{}
	for _, ct := range page.buyContainers {
		codes[ct.ID] = ct.Data.ID
	}
	byCode := map[string]priceState{}
	for _, st := range out.Data.States {
		byCode[codes[st.ID]] = st.Data
	}
	for i := range page.products {
		st, ok := byCode[page.products[i].Code]
		if !ok || st.Price == nil {
			continue
		}
		page.products[i].Price = st.Price.Current
		if st.Price.Previous != nil && st.Price.Current != nil && *st.Price.Previous > *st.Price.Current {
			page.products[i].OldPrice = st.Price.Previous
		}
	}
	return nil
}

type rawFilter struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	Label       string          `json:"label"`
	Count       *int            `json:"count"`
	Min         *float64        `json:"min"`
	Max         *float64        `json:"max"`
	MinSelected *float64        `json:"minSelected"`
	MaxSelected *float64        `json:"maxSelected"`
	Selected    json.RawMessage `json:"selected"`
	Variants    []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Count *int   `json:"count"`
	} `json:"variants"`
}

// parseFilters keeps the query-dependent filter keys, values and counts. Shop
// coordinates, images, hints and the fixed sort list are omitted.
func parseFilters(body []byte) ([]domain.Filter, error) {
	var out struct {
		Data struct {
			Blocks map[string][]rawFilter `json:"blocks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode filters: %w", err)
	}
	filters := []domain.Filter{}
	for _, block := range []string{"pageTop", "avails", "left"} {
		for _, f := range out.Data.Blocks[block] {
			if f.Type == "shops" || f.ID == "order" {
				continue
			}
			item := domain.Filter{Key: f.ID, Label: f.Label, Type: f.Type, Count: f.Count, Min: f.Min, Max: f.Max}
			if f.Type == "range-checkbox" {
				if f.MinSelected != nil || f.MaxSelected != nil {
					item.Selected = []*float64{f.MinSelected, f.MaxSelected}
				}
			} else if sel := selected(f.Selected); sel != nil {
				item.Selected = sel
			}
			if f.Type != "range-checkbox" {
				for _, v := range f.Variants {
					item.Values = append(item.Values, domain.FilterValue{ID: v.ID, Label: v.Label, Count: v.Count})
				}
			}
			if item.Key == "category" {
				item.Type = "category"
			}
			filters = append(filters, item)
		}
	}
	return filters, nil
}

func selected(raw json.RawMessage) any {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if b {
			return true
		}
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return list
	}
	return nil
}

// Details reads the characteristics fragment and the product microdata. A
// numeric product code is resolved through the storefront search first.
func (c *Client) Details(ctx context.Context, input string) (*domain.Details, error) {
	input = strings.TrimSpace(input)
	var u *url.URL
	if isCode(input) {
		resp, err := c.fetch(ctx, request{method: http.MethodGet, target: c.origin + "/search/?" + url.Values{"q": {input}}.Encode()})
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(resp.url.Path, "/product/") {
			return nil, fmt.Errorf("product code %s did not open a product page", input)
		}
		u = resp.url
	} else {
		var err error
		if u, err = c.pageURL(input); err != nil {
			return nil, err
		}
	}
	rest, ok := strings.CutPrefix(u.Path, "/product/")
	if !ok || strings.HasPrefix(rest, "characteristics/") || strings.Count(strings.Trim(rest, "/"), "/") != 1 {
		return nil, fmt.Errorf("details requires a /product/ID/SLUG/ URL or a numeric product code")
	}
	productURL := c.origin + "/product/" + rest
	chars, err := c.fetch(ctx, request{method: http.MethodGet, target: c.origin + "/product/characteristics/" + rest, xhr: true, referer: productURL})
	if err != nil {
		return nil, err
	}
	var fragment struct {
		Result bool   `json:"result"`
		HTML   string `json:"html"`
	}
	if err = json.Unmarshal(chars.body, &fragment); err != nil || !fragment.Result {
		return nil, fmt.Errorf("decode characteristics: unexpected response")
	}
	p, err := parseCharacteristics(fragment.HTML)
	if err != nil {
		return nil, err
	}
	p.URL = productURL
	micro, err := c.fetch(ctx, request{method: http.MethodGet, target: c.origin + "/product/microdata/" + p.ID + "/", xhr: true, referer: productURL})
	if err != nil {
		return nil, err
	}
	if err = applyMicrodata(&p, micro.body); err != nil {
		return nil, err
	}
	return &domain.Details{Product: p, HTTPRequests: c.requests}, nil
}

func isCode(s string) bool {
	if s == "" || len(s) > 12 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseCharacteristics(fragment string) (domain.Product, error) {
	doc, err := document([]byte(fragment))
	if err != nil {
		return domain.Product{}, err
	}
	p := domain.Product{Images: []string{}, Characteristics: []domain.Characteristic{}}
	p.ID = productGUID.FindString(doc.Find("[data-best-price-url]").AttrOr("data-best-price-url", ""))
	if p.ID == "" {
		return p, fmt.Errorf("characteristics fragment lacks the product ID")
	}
	doc.Find(".product-characteristics__group").Each(func(_ int, g *goquery.Selection) {
		group := strings.Join(strings.Fields(g.Find(".product-characteristics__group-title").First().Text()), " ")
		g.Find(".product-characteristics__spec").Each(func(_ int, s *goquery.Selection) {
			name := strings.Join(strings.Fields(s.Find(".product-characteristics__spec-title-content").First().Text()), " ")
			value := strings.Join(strings.Fields(s.Find(".product-characteristics__spec-value").First().Text()), " ")
			if name != "" && value != "" {
				p.Characteristics = append(p.Characteristics, domain.Characteristic{Group: group, Name: name, Value: value})
			}
		})
	})
	if len(p.Characteristics) == 0 {
		return p, fmt.Errorf("characteristics fragment has no specifications")
	}
	return p, nil
}

func applyMicrodata(p *domain.Product, body []byte) error {
	var out struct {
		Result bool `json:"result"`
		Data   struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			SKU         string   `json:"sku"`
			Image       []string `json:"image"`
			Brand       struct {
				Name string `json:"name"`
			} `json:"brand"`
			Offers struct {
				Price        *float64 `json:"price"`
				Availability string   `json:"availability"`
			} `json:"offers"`
			Rating *struct {
				Value *float64 `json:"ratingValue"`
				Count *int     `json:"reviewCount"`
			} `json:"aggregateRating"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil || !out.Result || out.Data.Name == "" {
		return fmt.Errorf("decode product microdata: unexpected response")
	}
	d := out.Data
	p.Title, p.Code, p.Brand, p.Price, p.Description = d.Name, d.SKU, d.Brand.Name, d.Offers.Price, clean(d.Description)
	p.Availability = strings.TrimPrefix(d.Offers.Availability, "https://schema.org/")
	if d.Rating != nil {
		p.Rating, p.ReviewCount = d.Rating.Value, d.Rating.Count
	}
	if d.Image != nil {
		p.Images = d.Image
	}
	return nil
}

// DefaultCity is the storefront city slug commands require unless --city
// names another one.
const DefaultCity = "kazan"

var citySlug = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

func ValidCity(slug string) bool { return citySlug.MatchString(slug) }

// StoreCity is the city the storefront prices and stocks pages for. The site
// keeps it in a server-signed cookie, so it can only be chosen in the browser.
type StoreCity struct {
	Slug string
	Name string
}

var cityNameRe = regexp.MustCompile(`"cityName":("(?:[^"\\]|\\.)*")`)

// SessionCity reads the imported city cookies without a request.
func SessionCity(s Session) StoreCity {
	var city StoreCity
	signed := false
	for _, c := range s.Cookies {
		switch c.Name {
		case "city_path":
			city.Slug = c.Value
		case "current_path":
			signed = true
			raw, err := url.QueryUnescape(c.Value)
			if err != nil {
				continue
			}
			if m := cityNameRe.FindStringSubmatch(raw); m != nil {
				_ = json.Unmarshal([]byte(m[1]), &city.Name)
			}
		}
	}
	if !signed {
		return StoreCity{}
	}
	return city
}

// RequireCity fails unless the imported session is pinned to slug.
func RequireCity(s Session, slug string) error {
	got := SessionCity(s)
	if got.Slug == slug {
		return nil
	}
	if got.Slug == "" {
		return fmt.Errorf("the imported session has no selected city; choose %s on %s in Chrome, then run auth import", slug, Origin)
	}
	return fmt.Errorf("the imported session city is %s (%s), not %s; switch the city on %s in Chrome, then run auth import", got.Slug, got.Name, slug, Origin)
}
