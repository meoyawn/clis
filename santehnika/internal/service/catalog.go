package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"github.com/meoyawn/clis/santehnika/internal/domain"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type soProduct struct {
	Countries   []string `json:"countries"`
	ID          int      `json:"id"`
	Code        string   `json:"code"`
	Title       string   `json:"title"`
	Link        string   `json:"link"`
	Brand       string   `json:"brand"`
	Price       *float64 `json:"price"`
	Images      []string `json:"images"`
	InStock     *bool    `json:"isInStock"`
	Purchasable *bool    `json:"isPurchasable"`
	Reviews     *struct {
		Rating *float64 `json:"rating"`
		Amount *int     `json:"amount"`
	} `json:"reviews"`
	ReviewStats *struct {
		Rating *float64 `json:"rating"`
		Amount *int     `json:"amount"`
	} `json:"reviewStats"`
	Features []struct {
		Title string          `json:"title"`
		Text  json.RawMessage `json:"text"`
	} `json:"fullFeatures"`
}
type soList struct {
	Total *int        `json:"totalCnt"`
	Data  []soProduct `json:"data"`
	Nav   struct {
		Page int `json:"page"`
		Size int `json:"pageElementCount"`
	} `json:"navParams"`
	Sorts json.RawMessage `json:"sort"`
}

func (c *Client) parseList(b []byte, target string, o Options) (*domain.Result, error) {
	var d soList
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("decode product list: %w", err)
	}
	if d.Total == nil {
		return nil, fmt.Errorf("catalog total is missing; response schema changed")
	}
	r := c.result(target)
	r.Total = d.Total
	r.Page = d.Nav.Page
	if r.Page == 0 {
		r.Page = o.Page
	}
	r.Sorts = d.Sorts
	r.HasMore = d.Nav.Size > 0 && r.Page*d.Nav.Size < *d.Total
	for _, p := range d.Data {
		u, _ := url.Parse(c.origin)
		link, _ := url.Parse(p.Link)
		out := domain.Product{ID: p.Code, Code: p.Code, APIID: strconv.Itoa(p.ID), Countries: p.Countries, Title: p.Title, URL: u.ResolveReference(link).String(), Brand: p.Brand, Price: p.Price, Currency: "RUB", Images: p.Images, InStock: p.InStock, Purchasable: p.Purchasable, Characteristics: []domain.Characteristic{}}
		if out.Images == nil {
			out.Images = []string{}
		}
		if p.Reviews != nil {
			out.ProductRating = p.Reviews.Rating
			out.ProductReviewCount = p.Reviews.Amount
			out.Rating = p.Reviews.Rating
			out.ReviewCount = p.Reviews.Amount
		}
		if p.ReviewStats != nil {
			out.Rating = p.ReviewStats.Rating
			out.ReviewCount = p.ReviewStats.Amount
		}
		for _, f := range p.Features {
			var text string
			var values []string
			if json.Unmarshal(f.Text, &text) != nil && json.Unmarshal(f.Text, &values) == nil {
				text = strings.Join(values, ", ")
			}
			out.Characteristics = append(out.Characteristics, domain.Characteristic{Name: f.Title, Value: clean(text)})
		}
		r.Products = append(r.Products, out)
	}
	trimResult(r, o.Limit)
	return r, nil
}
func soSort(o Options, q url.Values) error {
	field, order := "SORT", "DESC"
	switch o.Sort.String() {
	case "price":
		field, order = "PRICE", "ASC"
	case "price-desc":
		field = "PRICE"
	case "sale":
		field = "DISCOUNT_PERCENT"
	case "rating":
		return fmt.Errorf("santehnika has no rating sort; use popular, price, price-desc or sale")
	}
	q.Set("sort_field", field)
	q.Set("sort_order", order)
	return nil
}
func (c *Client) Search(ctx context.Context, query string, o Options) (*domain.Result, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search query must not be empty")
	}
	if o.IncludeFilters {
		u, _ := url.Parse(c.origin + "/search/")
		q := u.Query()
		q.Set("q", query)
		u.RawQuery = q.Encode()
		return c.List(ctx, u.String(), o)
	}
	q := url.Values{"PAGEN_1": {strconv.Itoa(o.Page)}, "searchText": {query}, "strategy": {"vectors_strict,zero_queries"}, "sectionId": {"search"}}
	if err := soSort(o, q); err != nil {
		return nil, err
	}
	for k, vs := range o.Filters {
		for i, v := range vs {
			q.Add(fmt.Sprintf("filter[%s][%d]", k, i), v)
		}
	}
	b, err := c.fetch(ctx, http.MethodPost, c.origin+"/ajax/react/searchProducts/", "application/x-www-form-urlencoded", []byte(q.Encode()), nil)
	if err != nil {
		return nil, err
	}
	display := c.origin + "/search/?q=" + url.QueryEscape(query)
	c.session.PageURL = display
	return c.parseList(b, display, o)
}

// Section lists use the exact single POST observed when changing website sorts.
func (c *Client) Section(ctx context.Context, section string, o Options) (*domain.Result, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if _, err := strconv.ParseUint(section, 10, 64); err != nil {
		return nil, fmt.Errorf("section must be a numeric ID from list output")
	}
	if o.IncludeFilters {
		return nil, fmt.Errorf("use list CATEGORY_URL --include-filters to read filters in one request")
	}
	q := url.Values{"pageType": {"category"}, "PAGEN_1": {strconv.Itoa(o.Page)}, "perpage": {"69"}, "sectionId": {section}}
	if err := soSort(o, q); err != nil {
		return nil, err
	}
	for k, vs := range o.Filters {
		for i, v := range vs {
			q.Add(fmt.Sprintf("filter[%s][%d]", k, i), v)
		}
	}
	target := c.origin + "/ajax/react/productList/"
	b, err := c.fetch(ctx, http.MethodPost, target, "application/x-www-form-urlencoded", []byte(q.Encode()), nil)
	if err != nil {
		return nil, err
	}
	return c.parseList(b, target, o)
}
func (c *Client) List(ctx context.Context, input string, o Options) (*domain.Result, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	u, err := c.pageURL(input)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(u.Path, "/product/") || strings.HasPrefix(u.Path, "/personal/") || strings.HasPrefix(u.Path, "/ajax/") {
		return nil, fmt.Errorf("list requires a public category or search URL")
	}
	q := u.Query()
	q.Del("PAGEN_1")
	if o.Page > 1 {
		q.Set("PAGEN_1", strconv.Itoa(o.Page))
	}
	q.Del("sort_field")
	q.Del("sort_order")
	if o.Sort.String() != "popular" {
		if err = soSort(o, q); err != nil {
			return nil, err
		}
	}
	for k, vs := range o.Filters {
		q.Del(k)
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	b, err := c.fetch(ctx, http.MethodGet, u.String(), "", nil, nil)
	if err != nil {
		return nil, err
	}
	doc, err := document(b)
	if err != nil {
		return nil, err
	}
	state, err := stateNode(doc, "stateSd")
	if err != nil {
		return nil, err
	}
	if err := checkPageKazan(state); err != nil {
		return nil, err
	}
	var d struct {
		Products json.RawMessage `json:"catalogProductsRaw"`
		Filters  json.RawMessage `json:"catalogFilterForm"`
	}
	if err = json.Unmarshal(state, &d); err != nil {
		return nil, fmt.Errorf("decode category state: %w", err)
	}
	r, err := c.parseList(d.Products, u.String(), o)
	if err != nil {
		return nil, err
	}
	if o.IncludeFilters {
		r.Filters = d.Filters
	}
	c.session.PageURL = u.String()
	return r, nil
}
func (c *Client) Details(ctx context.Context, input string, availability bool) (*domain.Result, error) {
	if availability {
		return nil, fmt.Errorf("santehnika details does not estimate regional delivery; use the published availability and verify the date in Chrome")
	}
	u, err := c.pageURL(input)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(u.Path, "/product/") {
		return nil, fmt.Errorf("details requires a /product/ URL from search/list")
	}
	b, err := c.fetch(ctx, http.MethodGet, u.String(), "", nil, nil)
	if err != nil {
		return nil, err
	}
	c.session.PageURL = u.String()
	return c.parseDetails(b, u.String())
}
func (c *Client) parseDetails(b []byte, target string) (*domain.Result, error) {
	doc, err := document(b)
	if err != nil {
		return nil, err
	}
	if state, err := stateNode(doc, "stateSd"); err == nil {
		if err := checkPageKazan(state); err != nil {
			return nil, err
		}
	}
	var data struct {
		Type        string   `json:"@type"`
		Name        string   `json:"name"`
		Code        string   `json:"sku"`
		Article     string   `json:"mpn"`
		Description string   `json:"description"`
		Images      []string `json:"image"`
		Brand       struct {
			Name string `json:"name"`
		} `json:"brand"`
		Offers struct {
			Price        *float64 `json:"price"`
			Currency     string   `json:"priceCurrency"`
			Availability string   `json:"availability"`
		} `json:"offers"`
		Aggregate struct {
			Rating *float64 `json:"ratingValue"`
			Count  *int     `json:"reviewCount"`
		} `json:"aggregateRating"`
	}
	doc.Find(`script[type="application/ld+json"]`).EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		candidate := data
		candidate.Type = ""
		candidate.Code = ""
		if json.Unmarshal([]byte(selection.Text()), &candidate) == nil && candidate.Type == "Product" && candidate.Code != "" {
			data = candidate
			return false
		}
		return true
	})
	if data.Type != "Product" || data.Code == "" {
		return nil, fmt.Errorf("product identity is missing; response schema changed")
	}
	out := domain.Product{ID: data.Code, Code: data.Code, Article: data.Article, Title: strings.TrimSpace(doc.Find("h1").First().Text()), URL: target, Brand: data.Brand.Name, Price: data.Offers.Price, Currency: data.Offers.Currency, Rating: data.Aggregate.Rating, ReviewCount: data.Aggregate.Count, Images: data.Images, Characteristics: []domain.Characteristic{}, Description: clean(data.Description)}
	if out.Title == "" {
		out.Title = data.Name
	}
	if out.Images == nil {
		out.Images = []string{}
	}
	if data.Offers.Availability != "" {
		available := strings.HasSuffix(data.Offers.Availability, "/InStock")
		out.InStock = &available
	}
	// Only two-column characteristic rows, never recommendations or review text.
	doc.Find(".text--sm").Each(func(_ int, s *goquery.Selection) {
		cs := s.ChildrenFiltered("div")
		if cs.Length() != 2 {
			return
		}
		name, value := clean(cs.Eq(0).Text()), clean(cs.Eq(1).Text())
		if name != "" && value != "" && len(name) < 100 {
			out.Characteristics = append(out.Characteristics, domain.Characteristic{Name: name, Value: value})
		}
	})
	r := c.result(target)
	r.Page = 1
	r.PageSize = 1
	total := 1
	r.Total = &total
	r.Products = append(r.Products, out)
	r.Warnings = append(r.Warnings, "Details contain server-rendered characteristics; check the dimension drawing for mounting dimensions. Rating may aggregate variants; product-specific count is unknown.")
	return r, nil
}

func checkPageKazan(state []byte) error {
	var page struct {
		GeoList struct {
			Data struct {
				Selected struct {
					ID int `json:"id"`
				} `json:"selected"`
			} `json:"data"`
		} `json:"geoList"`
	}
	if err := json.Unmarshal(state, &page); err != nil {
		return fmt.Errorf("decode page location: %w", err)
	}
	if id := page.GeoList.Data.Selected.ID; id != 0 && id != 1283 {
		return fmt.Errorf("catalog page belongs to city ID %d instead of Kazan (1283); import the Chrome session again", id)
	}
	return nil
}
