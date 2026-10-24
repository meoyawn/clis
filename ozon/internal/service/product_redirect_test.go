package service

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestProductCommandsFollowScopedRedirects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		full     bool
		reviews  bool
		requests int
	}{
		{name: "show", requests: 2},
		{name: "full show sections", full: true, requests: 8},
		{name: "reviews", reviews: true, requests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				path := r.URL.Query().Get("url")
				if !r.URL.Query().Has("__rr") {
					requireRequest(t, r)
					resp := response(nil, http.StatusTemporaryRedirect)
					resp.Header.Set("Location", entrypointPath+"?"+url.Values{"url": {path}, "__rr": {"1"}}.Encode())
					resp.Header.Add("Set-Cookie", "navigation=ready; Path=/; Secure")
					resp.Header.Add("Set-Cookie", "__Secure-refresh-token=never-forward; Path=/; Secure")
					return resp, nil
				}
				if cookie, err := r.Cookie("navigation"); err != nil || cookie.Value != "ready" {
					t.Fatal("redirect navigation cookie missing")
				}
				if _, err := r.Cookie("__Secure-refresh-token"); err == nil {
					t.Fatal("renewal cookie forwarded")
				}
				name := "product"
				switch {
				case strings.Contains(path, "/reviews/"):
					name = "reviews"
				case strings.Contains(path, "layout_container=pdpPage2column"):
					name = "details"
				case strings.HasPrefix(path, "/modal/otherOffersFromSellers?"):
					name = "sellers"
				}
				return response(fixture(t, name), http.StatusOK), nil
			})
			ref := ItemReference{SKU: "100", Path: "/product/100/"}
			var meta Metadata
			if tc.reviews {
				got, err := c.Reviews(t.Context(), ref, ReviewsOptions{Page: 1, Scope: SKUReviewScope(), Sort: UsefulReviewSort()}, RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if got.Scope != "sku" || got.Sort != "usefulness_desc" || len(got.Reviews) != 1 {
					t.Fatal("requested review scope or content lost")
				}
				meta = got.Metadata
			} else {
				got, err := c.Show(t.Context(), ref, ShowOptions{Full: tc.full}, RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if got.SKU != "100" || got.Prices.WithOzonCard.Amount != 232 || (tc.full && (got.Description == "" || len(got.Sellers) != 2)) {
					t.Fatal("product prices, details or seller offers lost")
				}
				meta = got.Metadata
			}
			if calls != tc.requests || meta.HTTPRequests != calls || len(meta.SourceURLs) != calls {
				t.Fatalf("calls=%d metadata=%+v", calls, meta)
			}
			// A later invocation must start from the saved cookie snapshot.
			if _, err := c.Show(t.Context(), ref, ShowOptions{}, RequestOptions{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProductRedirectScopeAndBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		target    string
		reviews   bool
		redirects int
		wantCalls int
		wantError string
	}{
		{name: "canonical same SKU", target: "/product/sample-100/?page_changed=true", redirects: 1, wantCalls: 2},
		{name: "external origin", target: "https://example.invalid/product/100/?secret=private", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "different SKU", target: "/product/101/?secret=private", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "different section", target: "/product/100/reviews/", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "new selection", target: "/product/100/?sku=101", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "review scope changed", reviews: true, target: "/product/100/reviews/?reviewsVariantMode=0&sort=usefulness_desc", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "review sort changed", reviews: true, target: "/product/100/reviews/?reviewsVariantMode=1&sort=score_asc", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "review page changed", reviews: true, target: "/product/100/reviews/?reviewsVariantMode=1&sort=usefulness_desc&page=2", redirects: 1, wantCalls: 1, wantError: "invalid product redirect"},
		{name: "loop", target: "/product/100/", redirects: 1, wantCalls: 1, wantError: "loop"},
		{name: "three redirects", redirects: 3, wantCalls: 4},
		{name: "too many redirects", redirects: 4, wantCalls: 4, wantError: "exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				requireRequest(t, r)
				if calls <= tc.redirects {
					target := tc.target
					if target == "" {
						target = entrypointPath + "?" + url.Values{"url": {"/product/100/"}, "__rr": {fmt.Sprint(calls)}}.Encode()
					}
					resp := response(nil, http.StatusTemporaryRedirect)
					resp.Header.Set("Location", target)
					return resp, nil
				}
				return response(fixture(t, "product"), http.StatusOK), nil
			})
			ref := ItemReference{SKU: "100", Path: "/product/100/"}
			if tc.reviews {
				got, err := c.Reviews(t.Context(), ref, ReviewsOptions{Page: 1, Scope: SKUReviewScope(), Sort: UsefulReviewSort()}, RequestOptions{})
				if calls != tc.wantCalls || got != nil || err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("calls=%d result=%+v err=%v", calls, got, err)
				}
				return
			}
			got, err := c.Show(t.Context(), ref, ShowOptions{}, RequestOptions{})
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
			if tc.wantError == "" {
				if err != nil || got.HTTPRequests != calls {
					t.Fatalf("result=%+v err=%v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), "private") || got != nil {
				t.Fatalf("result=%+v err=%v", got, err)
			}
		})
	}
}
