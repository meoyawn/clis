package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAvailabilityUsesKazanProductIDAndOneJSONRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"json", `{"pickupDate":"2026-11-02","price":0}`, false},
		{"html", `<html>Delivery calculation is unavailable</html>`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Path != "/ajax/react/deliveryCalculation/" || q.Get("location") != "Казань" || len(q["product_id[]"]) != 1 || q.Get("product_id[]") != "123456" || q.Get("set_product_id") != "123456" {
					t.Errorf("unexpected delivery request %s %s", r.Method, r.URL)
				}
				if !strings.HasPrefix(r.Header.Get("Accept"), "application/json") || r.Header.Get("Sec-Fetch-Mode") != "cors" || r.Header.Get("Sec-Fetch-Dest") != "empty" || r.Header.Get("Sec-Fetch-User") != "" || r.Header.Get("Upgrade-Insecure-Requests") != "" {
					t.Errorf("delivery request has navigation headers: %+v", r.Header)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := localCatalogClient(t, srv.URL)
			result, err := c.Availability(context.Background(), "123456")
			if requests != 1 {
				t.Fatalf("delivery calculation made %d requests", requests)
			}
			if tc.wantErr {
				if err == nil || result != nil || !strings.Contains(err.Error(), "invalid JSON") {
					t.Fatalf("invalid response must fail: result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var data struct {
				PickupDate string `json:"pickupDate"`
			}
			if err := json.Unmarshal(result.Availability, &data); err != nil {
				t.Fatal(err)
			}
			if result.ProductAPIID != "123456" || result.HTTPRequests != 1 || data.PickupDate != "2026-11-02" {
				t.Fatalf("incorrect availability result %+v data=%+v", result, data)
			}
		})
	}
}

func TestAvailabilityRejectsInvalidProductIDBeforeRequest(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer srv.Close()
	c := localCatalogClient(t, srv.URL)
	for _, id := range []string{"0", "-1", "not-an-id"} {
		result, err := c.Availability(context.Background(), id)
		if err == nil || result != nil || !strings.Contains(err.Error(), "api_id") || !strings.Contains(err.Error(), id) {
			t.Fatalf("invalid ID %q must identify the parameter: result=%+v err=%v", id, result, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid IDs made %d HTTP requests", requests)
	}
}
