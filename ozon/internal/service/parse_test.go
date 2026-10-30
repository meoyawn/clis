package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestSearchFields(t *testing.T) {
	t.Parallel()
	got, e := parseSearch(fixturePage(t, "search"), SearchOptions{Query: "test", Page: 1}, "/search/?text=test")
	if e != nil {
		t.Fatal(e)
	}
	if got.Count != 1 || got.Total != nil || !got.HasMore || *got.NextPage != 2 {
		t.Fatalf("pagination=%+v", got.Pagination)
	}
	p := got.Results[0]
	if p.SKU != "100" || p.Title != "Sample earplugs" || p.URL != "https://www.ozon.ru/product/sample-100/" || len(p.Images) != 1 {
		t.Fatalf("product=%+v", p)
	}
	if p.Price == nil || p.Price.Amount != 1234.5 || p.Rating == nil || *p.Rating != 4.8 || p.NumRatings == nil || *p.NumRatings != 3676 {
		t.Fatalf("price/rating=%+v", p)
	}
	if p.Quantity == nil || *p.Quantity != 961 || p.MaxOrderQuantity == nil || *p.MaxOrderQuantity != 700 || p.DeliveryDay != "Завтра" {
		t.Fatalf("inventory=%+v", p)
	}
	g, err := fixturePage(t, "search").first("tileGridDesktop")
	if err != nil {
		t.Fatal(err)
	}
	v := obj(arr(g["items"])[0])
	states := arr(v["mainState"])
	v["mainState"] = append(states[:2], states[3:]...)
	product, e := parseProduct(v)
	if e != nil {
		t.Fatal(e)
	}
	if product.Quantity != nil || product.MaxOrderQuantity == nil {
		t.Fatal("cart limits must not be presented as stock")
	}
}

func TestFiltersPreserveUsableValues(t *testing.T) {
	t.Parallel()
	got, e := parseFilters(fixturePage(t, "filters"), "test")
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Filters) != 5 {
		t.Fatal(got)
	}
	price := got.Filters[3]
	if price.Key != "currency_price" || price.Min == nil || *price.Min != 49.43 || price.Max == nil || *price.Max != 22385 || price.Options[0].Value != "49;250" {
		t.Fatalf("price=%+v", price)
	}
	if !got.Filters[4].OptionsTruncated || got.Filters[4].Options[0].Label != "Black" {
		t.Fatal(got.Filters[4])
	}
	if got.Filters[2].Multiple || !got.Filters[2].Options[0].Selected || got.Sorting[0].Value != "price" {
		t.Fatal(got)
	}
}

func TestFiltersRejectInvalidCategoryURLs(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "https://example.invalid/category/", "/category/%zz/"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			p := fixturePage(t, "filters")
			putWidget(t, p, "filtersDesktop-123-default-1", object{
				"sections": []any{object{"filters": []any{object{
					"type": "categoryFilter", "key": "category",
					"categoryFilter": object{
						"title":      "Category",
						"categories": []any{object{"title": "Protection", "urlValue": raw}},
					},
				}}}},
			})
			result, err := parseFilters(p, "test")
			if err == nil || !strings.Contains(err.Error(), "invalid category filter URL") || result != nil {
				t.Fatalf("expected category URL error, got result=%+v error=%v", result, err)
			}
		})
	}
}

func TestProductDetailsAndVariants(t *testing.T) {
	t.Parallel()
	got, e := parseShow(fixturePage(t, "product"), ItemReference{SKU: "100", Path: "/product/100/"})
	if e != nil {
		t.Fatal(e)
	}
	if got.Prices.WithOzonCard.Amount != 232 || got.Prices.WithoutOzonCard.Amount != 258 || got.Prices.Original != nil {
		t.Fatal(got.Prices)
	}
	if len(got.Images) != 2 || *got.Quantity != 494 || got.QuantitySource != "freeRest" {
		t.Fatal(got.Product)
	}
	if got.Variants[0].Variants[0].Value != "Blue" || !got.Variants[0].Variants[0].Selected || got.Variants[0].Variants[1].Availability != "outOfStock" {
		t.Fatal(got.Variants)
	}
	if e = parseDetails(fixturePage(t, "details"), got); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(got.Description, "Soft earplugs.") || strings.Contains(got.Description, "must not appear") || len(got.DescriptionImages) != 1 {
		t.Fatal(got.Description)
	}
	if len(got.Characteristics) != 2 || len(got.Characteristics[0].Values) != 2 || got.Characteristics[0].Group != "General" {
		t.Fatal(got.Characteristics)
	}
	if e = parseSellers(fixturePage(t, "sellers"), got, CheaperSellerView()); e != nil {
		t.Fatal(e)
	}
	if e = parseSellers(fixturePage(t, "sellers"), got, FasterSellerView()); e != nil {
		t.Fatal(e)
	}
	if len(got.Sellers) != 2 || len(got.Sellers[1].SeenIn) != 2 || got.Sellers[1].Prices.WithOzonCard.Amount != 196 || got.Sellers[1].Prices.WithoutOzonCard != nil || got.Sellers[1].DeliveryDay != "Доставим 24 сентября" {
		t.Fatal(got.Sellers)
	}
}

func TestReviewsMediaAndScopePagination(t *testing.T) {
	t.Parallel()
	got, e := parseReviews(fixturePage(t, "reviews"), ItemReference{SKU: "100", Path: "/product/100/"}, "/product/100/reviews/?reviewsVariantMode=1&sort=usefulness_desc")
	if e != nil {
		t.Fatal(e)
	}
	if *got.Total != 2191 || *got.NextPage != 2 || got.Rating == nil || *got.Rating != 4.8 {
		t.Fatal(got.Pagination)
	}
	r := got.Reviews[0]
	if r.ID != "review-1" || r.Author != "Example buyer" || r.Text != "Comfortable." || r.PublishedAt.Unix() != 1780066545 || *r.Rating != 5 {
		t.Fatal(r)
	}
	if len(r.Media) != 2 || r.Media[0].Type != "photo" || r.Media[0].Width != 900 || r.Media[1].Type != "video" || *r.Media[1].DurationSeconds != 91 || r.Media[1].PreviewURL == "" {
		t.Fatal(r.Media)
	}
	if r.Media[1].URL != "https://v-1.ozone.ru/video.mp4?type=review" {
		t.Fatal(r.Media[1])
	}
	u, err := url.Parse(got.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("page_key") != "opaque+=" || u.Query().Get("reviewsVariantMode") != "1" || u.Query().Get("sort") != "usefulness_desc" {
		t.Fatal(got.NextCursor)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret", "Account data", "do-not-export"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("exported unrelated account data")
		}
	}
}

func TestSchemaChangesFailLoudly(t *testing.T) {
	t.Parallel()
	if _, e := parseSearch(&page{WidgetStates: map[string]json.RawMessage{}}, SearchOptions{Query: "test", Page: 1}, "/search/?text=test"); e == nil {
		t.Fatal("missing grid accepted")
	}
	if _, e := parseSearch(fixturePage(t, "search"), SearchOptions{Query: "test", Page: 2}, "/search/?text=test&page=2"); e == nil {
		t.Fatal("wrong search page accepted")
	}
	if _, e := parseShow(fixturePage(t, "product"), ItemReference{SKU: "999", Path: "/product/999/"}); e == nil {
		t.Fatal("wrong product accepted")
	}
	if _, e := parseReviews(fixturePage(t, "reviews"), ItemReference{SKU: "100", Path: "/product/100/"}, "/product/100/reviews/?page=2"); e == nil {
		t.Fatal("wrong review page accepted")
	}
	p := fixturePage(t, "search")
	p.WidgetStates["tileGridDesktop-123-default-1"] = json.RawMessage(`"not-json"`)
	if _, e := p.first("tileGridDesktop"); e == nil {
		t.Fatal("invalid widget accepted")
	}
}

func TestExplicitEmptySearchDoesNotPageIntoRecommendations(t *testing.T) {
	t.Parallel()
	p := &page{WidgetStates: map[string]json.RawMessage{
		"statusWidget-1-default-1":             json.RawMessage(`{"titleAtom":{"text":"Ничего не нашлось","testInfo":{"automatizationId":"not-found-title"}},"messageAtom":{"text":"Попробуйте другой запрос"}}`),
		"infiniteVirtualPaginator-1-default-1": json.RawMessage(`{"nextPage":"/search/?text=test&non_found=1&page=2"}`),
	}}
	p.PageInfo.URL, p.PageInfo.PageType = "/search/?text=test", "search"
	r, err := parseSearch(p, SearchOptions{Query: "test", Page: 1}, "/search/?text=test")
	if err != nil || r.Count != 0 || r.Total == nil || *r.Total != 0 || r.HasMore || r.NextCursor != "" || r.Message == "" {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	f, err := parseFilters(p, "test")
	if err != nil || len(f.Filters) != 0 || f.Message == "" {
		t.Fatalf("result=%+v error=%v", f, err)
	}
}

func TestProductReference(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"100", "https://www.ozon.ru/product/100/", "https://ozon.ru/product/earplugs-100/?at=tracking", "https://www.ozon.ru/product/earplugs-100/reviews/?page=2"} {
		r, e := ParseItemReference(raw)
		if e != nil || r.SKU != "100" || strings.Contains(r.Path, "?") {
			t.Fatalf("%q: %+v %v", raw, r, e)
		}
	}
	for _, raw := range []string{"0", "-1", "https://www.ozon.ru.evil.test/product/100/", "https://user:pass@www.ozon.ru/product/100/", "https://www.ozon.ru:443/product/100/", "http://www.ozon.ru/product/100/", "https://www.ozon.ru/t/short", "https://www.ozon.ru/product/abc/"} {
		if _, e := ParseItemReference(raw); e == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
