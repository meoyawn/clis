package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/meoyawn/clis/lemana/internal/domain"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type lmProduct struct {
	ID     string `json:"productId"`
	Title  string `json:"displayedName"`
	Link   string `json:"productLink"`
	Brand  string `json:"brand"`
	Source string `json:"source"`
	Price  struct {
		Main     *float64 `json:"main_price"`
		Currency string   `json:"currency"`
	} `json:"price"`
	Description     string `json:"marketingDescription"`
	Characteristics []struct {
		Name  string `json:"description"`
		Value string `json:"value"`
	} `json:"characteristics"`
	Photo struct {
		Desktop string `json:"desktop"`
	} `json:"mediaMainPhoto"`
	Eligibility struct {
		Web *bool `json:"webEligible"`
	} `json:"eligibility"`
	Delivery json.RawMessage `json:"delivery"`
	Stores   json.RawMessage `json:"stores"`
}
type lmRating struct {
	ID          string   `json:"productId"`
	Rating      *float64 `json:"rating"`
	Count       *int     `json:"reviewNumber"`
	GroupRating *float64 `json:"groupRating"`
	GroupCount  *int     `json:"groupReviewNumber"`
}

func (c *Client) product(p lmProduct) domain.Product {
	u, _ := url.Parse(c.origin)
	link, _ := url.Parse(p.Link)
	out := domain.Product{ID: p.ID, Title: p.Title, URL: u.ResolveReference(link).String(), Brand: p.Brand, Price: p.Price.Main, Currency: p.Price.Currency, Purchasable: p.Eligibility.Web, Images: []string{}, Characteristics: []domain.Characteristic{}, Description: clean(p.Description), Delivery: p.Delivery}
	if p.Photo.Desktop != "" {
		out.Images = append(out.Images, p.Photo.Desktop)
	}
	for _, f := range p.Characteristics {
		out.Characteristics = append(out.Characteristics, domain.Characteristic{Name: f.Name, Value: f.Value})
		if f.Name == "Бренд" {
			out.Brand = f.Value
		}
	}
	return out
}
func (c *Client) Search(ctx context.Context, query string, o Options) (*domain.Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search query must not be empty")
	}
	u, _ := url.Parse(c.origin + "/search/")
	q := u.Query()
	q.Set("q", query)
	u.RawQuery = q.Encode()
	return c.List(ctx, u.String(), o)
}
func (c *Client) List(ctx context.Context, input string, o Options) (*domain.Result, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	u, err := c.pageURL(input)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(u.Path, "/catalogue/") && u.Path != "/search/" {
		return nil, fmt.Errorf("list URL must be a /catalogue/ or /search/ page")
	}
	q := u.Query()
	q.Del("loggingContext")
	q.Set("page", strconv.Itoa(o.Page))
	switch o.Sort.String() {
	case "popular":
		q.Set("sortby", "8")
	case "price":
		q.Set("sortby", "1")
	case "price-desc":
		q.Set("sortby", "2")
	case "rating":
		q.Set("sortby", "9")
	case "sale":
		q.Set("sortby", "10")
	}
	for key, values := range o.Filters {
		q.Set(key, strings.Join(values, "_"))
	}
	u.RawQuery = q.Encode()
	b, err := c.fetch(ctx, http.MethodGet, u.String(), "", nil, nil)
	if err != nil {
		return nil, err
	}
	return c.parseList(b, u.String(), o)
}
func initial(body []byte, app string, out any) error {
	doc, err := document(body)
	if err != nil {
		return err
	}
	b, err := stateNode(doc, "init")
	if err != nil {
		return err
	}
	marker := `window.INITIAL_STATE["` + app + `"]=`
	_, value, ok := strings.Cut(string(b), marker)
	if !ok {
		return fmt.Errorf("%s initial catalog state is missing", app)
	}
	if err = json.NewDecoder(strings.NewReader(value)).Decode(out); err != nil {
		return fmt.Errorf("decode %s catalog state: %w", app, err)
	}
	return nil
}
func (c *Client) parseList(body []byte, target string, o Options) (*domain.Result, error) {
	var state struct {
		PLP struct {
			PLP struct {
				Products struct {
					IDs     []string    `json:"productsIds"`
					Data    []lmProduct `json:"productsData"`
					Count   *int        `json:"productsCount"`
					Offset  int         `json:"offset"`
					Ratings struct {
						Data []lmRating `json:"data"`
					} `json:"ratings"`
					Sorts json.RawMessage `json:"availableSorts"`
				} `json:"products"`
				Facets struct {
					Definitions json.RawMessage `json:"facetDefinitions"`
				} `json:"facets"`
			} `json:"plp"`
		} `json:"plp"`
	}
	if err := initial(body, "plp", &state); err != nil {
		return nil, err
	}
	data := state.PLP.PLP.Products
	if data.Count == nil {
		return nil, fmt.Errorf("catalog total is missing; response schema changed")
	}
	if *data.Count > 0 && len(data.IDs) > 0 && len(data.Data) == 0 {
		return nil, fmt.Errorf("catalog returned product IDs without their data")
	}
	r := c.result(target)
	r.Total = data.Count
	r.Page = o.Page
	r.HasMore = data.Offset+len(data.IDs) < *data.Count
	r.Sorts = data.Sorts
	ratings := map[string]lmRating{}
	for _, v := range data.Ratings.Data {
		ratings[v.ID] = v
	}
	for _, p := range data.Data {
		out := c.product(p)
		v, found := ratings[p.ID]
		if found {
			setReviews(&out, v)
		}
		r.Products = append(r.Products, out)
	}
	if o.IncludeFilters {
		r.Filters = state.PLP.PLP.Facets.Definitions
	}
	trimResult(r, o.Limit)
	return r, nil
}
func (c *Client) Details(ctx context.Context, input string, availability bool) (*domain.Result, error) {
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
	r, key, source, err := c.parseDetails(b, u.String())
	if err != nil {
		return nil, err
	}
	if availability {
		if key == "" {
			return nil, fmt.Errorf("public stock API configuration is missing")
		}
		payload := rawJSON(map[string]any{"regionCode": "kazan", "productId": r.Products[0].ID, "unit": "шт.", "currencyKey": "RUB", "preferedStores": []string{}, "source": source})
		h := http.Header{"x-api-key": []string{key}, "Origin": []string{c.origin}}
		// The key is supplied by this public page; it is neither logged nor persisted.
		stocks, fetchErr := c.fetch(ctx, http.MethodPost, "https://api.lemanapro.ru/experience/LeroymerlinWebsite/v1/navigation-pdp-api/get-stocks?lang=ru", "application/json", payload, h)
		if fetchErr != nil {
			return nil, fetchErr
		}
		if !json.Valid(stocks) {
			return nil, fmt.Errorf("stock API returned invalid JSON")
		}
		r.Products[0].Availability = stocks
	}
	r.HTTPRequests = c.requests
	return r, nil
}
func (c *Client) parseDetails(body []byte, target string) (*domain.Result, string, string, error) {
	var state struct {
		PDP struct {
			PDP struct {
				Product struct {
					Info  lmProduct `json:"info"`
					Media struct {
						Images []struct {
							URL string `json:"url"`
						} `json:"images"`
						Manuals json.RawMessage `json:"manuals"`
					} `json:"media"`
				} `json:"product"`
				Reviews struct {
					Reviews struct {
						Rating struct {
							Total struct {
								Score      *float64 `json:"score"`
								Count      *int     `json:"count"`
								GroupScore *float64 `json:"groupScore"`
								GroupCount *int     `json:"groupReviewCount"`
							} `json:"total"`
						} `json:"rating"`
						Cards json.RawMessage `json:"reviewsCards"`
					} `json:"reviews"`
				} `json:"reviews"`
			} `json:"pdp"`
			Env struct {
				APIKey string `json:"API_KEY"`
			} `json:"env"`
		} `json:"pdp"`
	}
	if err := initial(body, "pdp", &state); err != nil {
		return nil, "", "", err
	}
	p := state.PDP.PDP.Product.Info
	if p.ID == "" || p.Title == "" {
		return nil, "", "", fmt.Errorf("product identity is missing; response schema changed")
	}
	out := c.product(p)
	out.URL = target
	for _, im := range state.PDP.PDP.Product.Media.Images {
		if im.URL != "" {
			out.Images = append(out.Images, im.URL)
		}
	}
	out.Documents = state.PDP.PDP.Product.Media.Manuals
	out.Reviews = state.PDP.PDP.Reviews.Reviews.Cards
	v := state.PDP.PDP.Reviews.Reviews.Rating.Total
	setReviews(&out, lmRating{Rating: v.Score, Count: v.Count, GroupRating: v.GroupScore, GroupCount: v.GroupCount})
	r := c.result(target)
	r.Page = 1
	total := 1
	r.Total = &total
	r.PageSize = 1
	r.Products = append(r.Products, out)
	return r, state.PDP.Env.APIKey, p.Source, nil
}

func setReviews(out *domain.Product, v lmRating) {
	out.Rating = displayRating(v.Rating)
	out.ReviewCount = v.Count
	if v.Count != nil && *v.Count == 0 {
		out.Rating = nil
	}
	out.ProductRating = out.Rating
	out.ProductReviewCount = out.ReviewCount
	out.GroupRating = displayRating(v.GroupRating)
	out.GroupReviewCount = v.GroupCount
	if v.GroupCount != nil && *v.GroupCount == 0 {
		out.GroupRating = nil
	}
}

func displayRating(value *float64) *float64 {
	if value == nil || *value <= 0 {
		return nil
	}
	rounded := math.Round(*value*100) / 100
	return &rounded
}
