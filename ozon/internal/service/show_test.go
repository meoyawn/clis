package service

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestShowLightweightUsesOnlyMainPage(t *testing.T) {
	t.Parallel()
	for _, detailsPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "details absent", true: "details on main page"}[detailsPresent], func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if path := requireRequest(t, r); path != "/product/100/" || calls != 1 {
					t.Fatal("lightweight show fetched an additional section")
				}
				p := fixturePage(t, "product")
				if detailsPresent {
					for key, widget := range fixturePage(t, "details").WidgetStates {
						p.WidgetStates[key] = widget
					}
				}
				body, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(body, http.StatusOK), nil
			})
			got, err := c.Show(t.Context(), ItemReference{SKU: "100", Path: "/product/100/"}, ShowOptions{}, RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.HTTPRequests != 1 || got.Full || len(got.Warnings) != 0 {
				t.Fatalf("unexpected lightweight metadata: %+v", got)
			}
			if got.Prices.WithOzonCard.Amount != 232 || got.Prices.WithoutOzonCard.Amount != 258 || len(got.Variants) != 1 || len(got.Sellers) != 1 || !got.Sellers[0].Current {
				t.Fatal("main-page prices, variants or current seller lost")
			}
			if (got.Description != "") != detailsPresent || (len(got.Characteristics) > 0) != detailsPresent {
				t.Fatal("lightweight show did not retain the available main-page details")
			}
		})
	}
}

func TestShowFullSkipsAbsentOptionalSections(t *testing.T) {
	t.Parallel()
	calls := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		requireRequest(t, r)
		if calls != 1 {
			t.Fatal("invented a request for an absent section")
		}
		p := fixturePage(t, "product")
		delete(p.WidgetStates, "paginator-123-default-1")
		delete(p.WidgetStates, "webBestSeller-123-default-1")
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return response(body, http.StatusOK), nil
	})
	got, err := c.Show(t.Context(), ItemReference{SKU: "100", Path: "/product/100/"}, ShowOptions{Full: true}, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Full || got.HTTPRequests != 1 || len(got.Sellers) != 1 || len(got.Warnings) != 1 {
		t.Fatalf("unexpected full result with absent sections: %+v", got)
	}
}
