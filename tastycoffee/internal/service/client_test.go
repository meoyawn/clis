package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func testSession() Session {
	return Session{Cookie: SavedCookie{Name: "auth.token", Value: "SYNTHETIC_TOKEN", Domain: "shop.tastycoffee.ru", Path: "/"}}
}

// Catalog values intentionally disagree with historical order prices. The
// returned order must retain the purchase terms and discard catalog payloads.
func TestOrdersPurchaseTerms(t *testing.T) {
	client, err := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" || req.URL.Host != "shop.tastycoffee.ru" || req.URL.Path != "/api/v2/account/orders" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		if req.URL.Query().Get("page") != "2" || req.URL.Query().Get("limit") != "3" || req.URL.Query().Get("current") != "true" || req.Header.Get("Authorization") != "Bearer SYNTHETIC_TOKEN" {
			t.Fatal("pagination, active-order filter, or authentication missing")
		}
		return jsonResponse(`{"data":[{"id":120,"created_at":"2026-01-02T10:00:00Z","total_price":850,"is_paid":false,"status":{"name":"Processing"},"products":[{"product_id":7,"product_name":"Synthetic coffee","count":1,"price":1000,"product_options":[{"product_option_id":10,"product_option_value_id":11,"name":"Grind","value":"Whole beans"}],"product":{"price":9999,"discount":50,"mini_description":"CURRENT_CATALOG"}}],"prices":[{"code":"discount_tasty","name":"Personal discount 15%","value":"-150.00"}],"delivery":{"code":"fivepost.1","name":"5post","full_address":"Synthetic pickup point","method":"Pickup","track_url":null}}],"meta":{"current_page":2,"last_page":4,"total":10}}`), nil
	})}, testSession())
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Orders(t.Context(), 2, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if page.Page != 2 || page.Pages != 4 || page.Total != 10 || len(page.Orders) != 1 {
		t.Fatalf("wrong order pagination: %+v", page)
	}
	order := page.Orders[0]
	if order.ID != 120 || order.Total != 850 || order.Paid || order.Status != "Processing" || len(order.Products) != 1 {
		t.Fatalf("wrong purchase terms: %+v", order)
	}
	product := order.Products[0]
	if product.Price != 1000 || product.ID != 7 || product.Quantity != 1 || len(product.Options) != 1 || product.Options[0].ID != 11 || product.Options[0].Value != "Whole beans" {
		t.Fatalf("wrong historical product or option: %+v", product)
	}
	if len(order.Prices) != 1 || order.Prices[0].Value != "-150.00" || order.Delivery.Address != "Synthetic pickup point" || order.Delivery.Code != "fivepost.1" {
		t.Fatalf("lost discount or pickup terms: %+v", order)
	}
}

func TestLatestAcrossActiveAndHistory(t *testing.T) {
	for _, tt := range []struct {
		name, active, history string
		want                  int64
	}{
		{"active newer", `{"id":20,"created_at":"2026-02-01T10:00:00Z"}`, `{"id":10,"created_at":"2026-01-01T10:00:00Z"}`, 20},
		{"history newer", `{"id":10,"created_at":"2026-01-01T10:00:00Z"}`, `{"id":20,"created_at":"2026-02-01T10:00:00Z"}`, 20},
		{"no active orders", "", `{"id":10,"created_at":"2026-01-01T10:00:00Z"}`, 10},
		{"no orders", "", "", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seen := map[bool]bool{}
			client, err := NewClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				active := req.URL.Query().Get("current") == "true"
				seen[active] = true
				item := tt.history
				if active {
					item = tt.active
				}
				return jsonResponse(fmt.Sprintf(`{"data":[%s],"meta":{"current_page":1,"last_page":1,"total":1}}`, item)), nil
			})}, testSession())
			if err != nil {
				t.Fatal(err)
			}
			order, err := client.Latest(t.Context())
			if tt.want == 0 {
				if err == nil || !strings.Contains(err.Error(), "no orders") {
					t.Fatalf("expected no-orders error, got %+v, %v", order, err)
				}
				return
			}
			if err != nil || order.ID != tt.want || !seen[true] || !seen[false] {
				t.Fatalf("latest = %+v, %v; queried lists = %v", order, err, seen)
			}
		})
	}
}

func TestAPIErrorsDoNotRetryOrLeak(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		body    string
		content string
		want    string
	}{
		{"unauthorized", 401, "PRIVATE_ERROR_DETAIL", "application/json", "auth import"},
		{"redirect", 302, "PRIVATE_ERROR_DETAIL", "text/html", "HTTP 302"},
		{"challenge", 200, "PRIVATE_ERROR_DETAIL", "text/html", "non-JSON"},
		{"malformed JSON", 200, "PRIVATE_ERROR_DETAIL", "application/json", "invalid JSON"},
		{"changed envelope", 200, `{"message":"PRIVATE_ERROR_DETAIL"}`, "application/json", "account ID"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			client, err := NewClient(&http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tt.status, Header: http.Header{"Content-Type": {tt.content}, "Location": {"https://unrelated.example/"}}, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})}, testSession())
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Account(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "PRIVATE_ERROR_DETAIL") || strings.Contains(err.Error(), "SYNTHETIC_TOKEN") || calls != 1 {
				t.Fatalf("error = %v; requests = %d", err, calls)
			}
		})
	}
}
