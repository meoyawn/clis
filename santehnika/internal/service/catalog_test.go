package service

import (
	"context"
	"github.com/meoyawn/clis/santehnika/internal/domain"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSearchUsesOneJSONRequestAndPreservesRatingScopes(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Origin") != "http://"+r.Host || !strings.HasPrefix(r.Header.Get("Accept"), "application/json") {
			t.Errorf("catalog POST lacks browser request context: Origin=%q Accept=%q", r.Header.Get("Origin"), r.Header.Get("Accept"))
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Method != "POST" || r.URL.Path != "/ajax/react/searchProducts/" || r.PostForm.Get("searchText") != "смеситель" || r.PostForm.Get("filter[BRAND][0]") != "499" || r.PostForm.Get("sort_field") != "PRICE" || r.PostForm.Get("sort_order") != "ASC" {
			t.Errorf("unexpected request %s %s %+v", r.Method, r.URL, r.PostForm)
		}
		_, _ = w.Write(fixture(t, "list.json"))
	}))
	defer srv.Close()
	c := localCatalogClient(t, srv.URL)
	result, err := c.Search(context.Background(), "смеситель", Options{Page: 1, Sort: domain.Price(), Filters: url.Values{"BRAND": {"499"}}})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.HTTPRequests != 1 || len(result.Products) != 2 || !result.HasMore {
		t.Fatalf("result %+v requests=%d", result, requests)
	}
	p := result.Products[0]
	if *p.ProductReviewCount != 2 || *p.ReviewCount != 15 || *p.Rating != 4.13 || len(p.Characteristics) != 2 || p.Characteristics[1].Value != "13.2 см" {
		t.Fatalf("incorrect product %+v", p)
	}
	if result.Products[1].ReviewCount != nil || result.Products[1].Rating != nil {
		t.Fatal("unknown rating was fabricated")
	}
}
func TestHTMLListIncludesFiltersWithoutAnotherCall(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.RawQuery != "" {
			t.Errorf("default category page must use its canonical URL, got %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write(fixture(t, "list.html"))
	}))
	defer srv.Close()
	c := localCatalogClient(t, srv.URL)
	result, err := c.List(context.Background(), "/smesiteli/na_kukhnyu/", Options{Page: 1, Sort: domain.Popular(), IncludeFilters: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.HTTPRequests != 1 || len(result.Filters) == 0 || result.PageSize != 2 || len(result.Products) != 1 {
		t.Fatalf("unexpected list %+v", result)
	}
}
func TestProductIgnoresOtherJSONLDAndExtractsSSRCharacteristics(t *testing.T) {
	c := &Client{origin: Origin}
	result, err := c.parseDetails(fixture(t, "product.html"), Origin+"/product/test/343831/")
	if err != nil {
		t.Fatal(err)
	}
	p := result.Products[0]
	if p.ID != "343831" || p.Code != "343831" || p.Article != "BA365AA" || p.Brand != "Vidima" || *p.ReviewCount != 4 || p.ProductReviewCount != nil || len(p.Characteristics) != 2 || len(p.Images) != 1 || *p.Price != 12000 {
		t.Fatalf("incorrect details %+v", p)
	}
}

func TestHTMLListRejectsForeignRegion(t *testing.T) {
	body := strings.Replace(string(fixture(t, "list.html")), `{"catalogProductsRaw":`, `{"geoList":{"data":{"selected":{"id":2097,"name":"Москва"}}},"catalogProductsRaw":`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer srv.Close()
	c := localCatalogClient(t, srv.URL)
	result, err := c.List(context.Background(), "/smesiteli/na_kukhnyu/", Options{Page: 1, Sort: domain.Popular()})
	if err == nil || result != nil || !strings.Contains(err.Error(), "Kazan") {
		t.Fatalf("foreign region must not be reported as Kazan: err=%v", err)
	}
}
