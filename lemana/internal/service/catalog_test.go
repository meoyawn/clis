package service

import (
	"context"
	"encoding/json"
	"github.com/meoyawn/clis/lemana/internal/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestListUsesOneResponseForProductsFiltersAndRealRatings(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/catalogue/kuhonnye-smesiteli/" || r.URL.Query().Get("10844") != "Германия_Испания" || r.URL.Query().Get("sortby") != "9" || r.URL.Query().Get("page") != "2" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		_, _ = w.Write(fixture(t, "list.html"))
	}))
	defer srv.Close()
	c := &Client{http: srv.Client(), origin: srv.URL, session: Session{UserAgent: BrowserUserAgent}}
	result, err := c.List(context.Background(), "/catalogue/kuhonnye-smesiteli/", Options{Page: 2, Limit: 3, Sort: domain.Rating(), IncludeFilters: true, Filters: url.Values{"10844": {"Германия", "Испания"}}})
	if err != nil {
		t.Fatal(err)
	}
	p := result.Products[0]
	if requests != 1 || result.HTTPRequests != 1 || result.PageSize != 4 || len(result.Products) != 3 || !result.HasMore || len(result.Filters) == 0 {
		t.Fatalf("unexpected result %+v requests=%d", result, requests)
	}
	if p.ProductReviewCount == nil || *p.ProductReviewCount != 2 || p.ReviewCount == nil || *p.ReviewCount != 2 || p.Rating == nil || *p.Rating != 4 {
		t.Fatalf("wrong exact/group ratings %+v", p)
	}
	if p.GroupReviewCount == nil || *p.GroupReviewCount != 11 || p.GroupRating == nil || *p.GroupRating != 4.7 {
		t.Fatalf("group ratings were not kept separately: %+v", p)
	}
	if p := result.Products[1]; p.ReviewCount == nil || *p.ReviewCount != 0 || p.Rating != nil {
		t.Fatalf("zero own reviews must not inherit the group's reviews: %+v", p)
	}
	if p := result.Products[2]; p.ReviewCount != nil || p.Rating != nil {
		t.Fatalf("unknown own reviews must not inherit the group's reviews: %+v", p)
	}
	if result.Products[1].GroupReviewCount == nil || result.Products[2].GroupReviewCount == nil || *result.Products[1].GroupReviewCount != 59 || *result.Products[2].GroupReviewCount != 80 {
		t.Fatal("group counters were lost for products without own reviews")
	}
	if len(p.Characteristics) == 0 || p.Price == nil || len(p.Images) != 1 {
		t.Fatalf("missing product data %+v", p)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDetailsStocksAreOneOptionalRequest(t *testing.T) {
	for _, withStocks := range []bool{false, true} {
		t.Run(map[bool]string{false: "details", true: "with-stocks"}[withStocks], func(t *testing.T) {
			gets, posts := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					gets++
					_, _ = w.Write(fixture(t, "product.html"))
					return
				}
				posts++
				if r.URL.Path != "/experience/LeroymerlinWebsite/v1/navigation-pdp-api/get-stocks" || r.Header.Get("x-api-key") != "synthetic-public-client-key" {
					t.Errorf("unexpected stock request %s", r.URL)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["regionCode"] != "kazan" || payload["productId"] != "90545774" {
					t.Errorf("stock payload %+v", payload)
				}
				_, _ = io.WriteString(w, `{"stores":[{"name":"Казань","quantity":3}]}`)
			}))
			defer srv.Close()
			endpoint, _ := url.Parse(srv.URL)
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.URL.Scheme = endpoint.Scheme
				r.URL.Host = endpoint.Host
				return transport.RoundTrip(r)
			})}
			c := &Client{http: client, origin: srv.URL, session: Session{UserAgent: BrowserUserAgent}}
			result, err := c.Details(context.Background(), "/product/test-90545774/", withStocks)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if withStocks {
				expected = 2
			}
			if gets != 1 || posts != expected-1 || result.HTTPRequests != expected {
				t.Fatalf("gets=%d posts=%d requests=%d", gets, posts, result.HTTPRequests)
			}
			p := result.Products[0]
			if *p.ProductReviewCount != 2 || *p.ReviewCount != 2 || p.Rating == nil || *p.Rating != 4 || !strings.Contains(string(p.Reviews), "Synthetic catalog review") || len(p.Images) < 2 {
				t.Fatalf("missing detail data %+v", p)
			}
			if p.GroupReviewCount == nil || *p.GroupReviewCount != 11 || p.GroupRating == nil || *p.GroupRating != 4.8 {
				t.Fatalf("group ratings were not kept separately: %+v", p)
			}
			if (len(p.Availability) > 0) != withStocks {
				t.Fatalf("unexpected availability %s", p.Availability)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "synthetic-public-client-key") {
				t.Fatal("public API configuration leaked to output")
			}
		})
	}
}
