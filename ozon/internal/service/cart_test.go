package service

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCartNavigationKeepsReadAndWriteScope(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"list", "add", "remove"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			calls := 0
			var firstBody string
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				body := ""
				if r.Body != nil {
					b, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					body = string(b)
				}
				if calls == 1 {
					firstBody = body
					q := r.URL.Query()
					q.Set("__rr", "1")
					next := *r.URL
					next.RawQuery = q.Encode()
					res := response(nil, 307)
					res.Header.Set("Location", next.String())
					res.Header.Add("Set-Cookie", "navigation=ready; Path=/; Secure")
					res.Header.Add("Set-Cookie", "__Secure-access-token=must-not-replace-account; Path=/; Secure")
					return res, nil
				}
				if calls != 2 || r.URL.Query().Get("__rr") != "1" || body != firstBody || !strings.Contains(r.Header.Get("Cookie"), "navigation=ready") || !strings.Contains(r.Header.Get("Cookie"), "session=secret-cookie-value") || strings.Contains(r.Header.Get("Cookie"), "must-not-replace-account") {
					t.Fatal("cart navigation changed payload, credentials or request bounds")
				}
				if command == "list" {
					if r.Method != "GET" || r.URL.Path != entrypointPath || r.URL.Query().Get("url") != "/cart" {
						t.Fatal("cart read changed scope")
					}
					return response(fixture(t, "cart"), 200), nil
				}
				if r.Method != "POST" {
					t.Fatal("cart write changed method")
				}
				if command == "remove" {
					if r.URL.Path != entrypointPath || r.URL.Query().Get("url") != "/cart?page_changed=true" {
						t.Fatal("cart deletion changed scope")
					}
					return response(fixture(t, "cart-empty"), 200), nil
				}
				if r.URL.Path != addToCartPath {
					t.Fatal("cart addition changed endpoint")
				}
				return response([]byte(`{"data":{"success":true,"cart":{"cartItems":[{"id":100,"qty":1}]}}}`), 200), nil
			})
			ref := ItemReference{SKU: "100"}
			var err error
			var requests int
			switch command {
			case "list":
				result, e := c.CartList(t.Context(), RequestOptions{})
				err = e
				if result != nil {
					requests = result.HTTPRequests
				}
			case "add":
				result, e := c.CartAdd(t.Context(), ref, RequestOptions{})
				err = e
				if result != nil {
					requests = result.HTTPRequests
				}
			case "remove":
				result, e := c.CartRemove(t.Context(), ref, RequestOptions{})
				err = e
				if result != nil {
					requests = result.HTTPRequests
				}
			}
			if err != nil || calls != 2 || requests != 2 {
				t.Fatalf("error=%v requests=%d actual=%d", err, requests, calls)
			}
		})
	}
}

func TestCartWritesRejectChangedRedirectsAndBoundNavigation(t *testing.T) {
	t.Parallel()
	for _, redirect := range []string{
		"https://example.com/api/composer-api.bx/_action/v2/addToCart?__rr=1",
		WebOrigin + "/api/composer-api.bx/_action/deleteItems?__rr=1",
		WebOrigin + addToCartPath + "?__rr=1&quantity=5",
		WebOrigin + addToCartPath + "?__rr=1&__rr=2",
		"bounded",
	} {
		t.Run(redirect, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				res := response(nil, 307)
				location := redirect
				if redirect == "bounded" {
					q := url.Values{"__rr": {strconv.Itoa(calls)}}
					location = WebOrigin + addToCartPath + "?" + q.Encode()
				}
				if r.Method != "POST" {
					t.Fatal("mutation became a read")
				}
				res.Header.Set("Location", location)
				return res, nil
			})
			_, err := c.CartAdd(t.Context(), ItemReference{SKU: "100"}, RequestOptions{})
			wantCalls := 1
			if redirect == "bounded" {
				wantCalls = 4
			}
			if err == nil || calls != wantCalls {
				t.Fatalf("error=%v requests=%d want=%d", err, calls, wantCalls)
			}
		})
	}
}
