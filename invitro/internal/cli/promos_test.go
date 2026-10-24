package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const promoSlug = "biohimiya-krovi-09-2026-rf"

func promoNow() time.Time { return time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC) }

func promoHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials in public request")
		}
		switch r.URL.Path {
		case "/golk/addresses/api/v1/cities":
			writeFixture(t, w, "city")
		case "/golk/cms/cms-proxy/promotions/filtered":
			if r.URL.Query().Get("cityId") != testCityID || r.URL.Query().Get("targetPage") != "promotions" {
				t.Error("incorrect promotion filter")
			}
			writeFixture(t, w, "promotions")
		case "/golk/cms/cms-proxy/pages/filtered":
			if r.URL.Query().Get("cityId") != testCityID || r.URL.Query().Get("slug") != promoSlug {
				t.Error("incorrect promotion page query")
			}
			writeFixture(t, w, "promotion-page")
		case "/golk/tests/api/v1/complexes/by-article":
			if r.URL.Query().Get("cityID") != testCityID {
				t.Error("incorrect article city")
			}
			var p map[string]any
			if err := json.Unmarshal(fixture(t, "promotion-article"), &p); err != nil {
				t.Fatal(err)
			}
			switch r.URL.Query().Get("article") {
			case "АКЦ2574":
			case "АКЦ2609":
				p["id"] = "7ac70f54-b194-f111-a65c-00155d52d169"
				p["code"] = "АКЦ2609"
				p["price"] = 2325
			default:
				t.Error("unexpected article")
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(p); err != nil {
				t.Errorf("encode fixture response: %v", err)
			}
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}
}

func TestPromotionPricesAndComposition(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			c := testClient(t, promoHandler(t))
			city, err := c.resolveCity(context.Background(), "kazan")
			if err != nil {
				t.Fatal(err)
			}
			list, err := c.Promotions(context.Background(), city, 1, full, promoNow())
			if err != nil {
				t.Fatal(err)
			}
			if list.Count != 7 || list.APITotal != 12 || list.HTTPRequests != 2 || list.Results[0].Slug != promoSlug {
				t.Fatalf("wrong list: %+v", list)
			}
			detail, err := c.Promotion(context.Background(), city, list.Results[0], full)
			if err != nil {
				t.Fatal(err)
			}
			if detail.HTTPRequests != 5 || len(detail.Offers) != 2 {
				t.Fatalf("wrong prices/request count: %+v", detail)
			}
			first := detail.Offers[0]
			if first.ID != first.CatalogID || first.Code != "АКЦ2574" || first.PriceFrom == nil || *first.PriceFrom != 1465 || first.URL != detail.URL || first.AdditionalServices == nil || first.OldPrice != nil {
				t.Fatalf("promotion article not normalized: %+v", first)
			}
			// The upstream is_promo flag is false even for these promotion-only articles.
			if first.Promo == nil || *first.Promo {
				t.Fatal("rewrote upstream promo flag")
			}
			if first.LoyaltyBonus == nil || *first.LoyaltyBonus != 0 {
				t.Fatal("lost published loyalty bonus")
			}
			if (len(detail.Source) > 0) != full || (len(first.Source) > 0) != full || (len(list.Source) > 0) != full {
				t.Fatal("wrong full mode")
			}
			text := ""
			for _, s := range detail.Sections {
				text += s.Title + "\n" + s.Text + "\n"
			}
			for _, term := range []string{"8 показателей", "№ 22", "Креатинин", "№ 39", "15%"} {
				if !strings.Contains(text, term) {
					t.Errorf("missing composition/terms %q", term)
				}
			}
			if !strings.Contains(detail.Links[0], "rules_krov") || !strings.Contains(strings.Join(detail.Links, " "), "/offices/kazan/") {
				t.Fatal("missing rules/city links")
			}
		})
	}
}

func TestCMSRichTextAndLegacyFallback(t *testing.T) {
	var value any
	if err := json.Unmarshal([]byte(`{"textBlockContent":[{"content":{"root":{"type":"root","children":[{"type":"paragraph","children":[{"text":"Current "},{"text":"offer"}]},{"type":"paragraph","children":[{"text":"New terms"}]}]}}}],"textBlockDescription":"Expired legacy terms","cards":[{"label":"Creatinine","caption":"Included","icon":{"title":"Hidden icon"}}]}`), &value); err != nil {
		t.Fatal(err)
	}
	text := cmsText(value)
	if !strings.Contains(text, "Current offer\nNew terms") || !strings.Contains(text, "Creatinine\nIncluded") || strings.Contains(text, "Expired") || strings.Contains(text, "Hidden") {
		t.Fatalf("bad projection %q", text)
	}
	if got := cmsText(map[string]any{"textBlockDescription": "Legacy only"}); got != "Legacy only" {
		t.Fatalf("lost legacy content %q", got)
	}
}

func TestPromotionEligibilityAndPagination(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutation func(map[string]any)
		want     int
		fails    bool
	}{
		{"expired", func(p map[string]any) { p["endDate"] = "2026-09-18T00:00:00Z" }, 6, false},
		{"future", func(p map[string]any) { p["startDate"] = "2026-10-01T00:00:00Z" }, 6, false},
		{"disabled", func(p map[string]any) { p["enabled"] = false }, 6, false},
		{"draft", func(p map[string]any) { p["_status"] = "draft" }, 6, false},
		{"app-only", func(p map[string]any) { p["platform"] = []string{"app"} }, 6, false},
		{"missing-title", func(p map[string]any) { p["title"] = "" }, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal(fixture(t, "promotions"), &raw); err != nil {
				t.Errorf("decode fixture: %v", err)
				return
			}
			test.mutation(raw["docs"].([]any)[0].(map[string]any))
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(raw); err != nil {
					t.Errorf("encode fixture response: %v", err)
				}
			})
			got, err := c.Promotions(context.Background(), cityInfo{ID: testCityID, Slug: "kazan"}, 1, false, promoNow())
			if (err != nil) != test.fails {
				t.Fatalf("unexpected error %v", err)
			}
			if err == nil && got.Count != test.want {
				t.Fatalf("count %d", got.Count)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"docs":[],"totalDocs":0,"page":1,"totalPages":1,"hasNextPage":true}`, `{"docs":[],"totalDocs":0,"page":2,"totalPages":1}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, raw)
		})
		if _, err := c.Promotions(context.Background(), cityInfo{ID: testCityID, Slug: "kazan"}, 1, false, promoNow()); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPromotionUnknownPricesIdentityAndLimit(t *testing.T) {
	for _, test := range []string{"null-price", "wrong-code", "negative-price", "wrong-page", "too-many", "unknown-type"} {
		t.Run(test, func(t *testing.T) {
			base := promoHandler(t)
			articles := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "pages/filtered") && (test == "wrong-page" || test == "too-many" || test == "unknown-type") {
					var raw map[string]any
					if err := json.Unmarshal(fixture(t, "promotion-page"), &raw); err != nil {
						t.Errorf("decode fixture: %v", err)
						return
					}
					if test == "wrong-page" {
						raw["slug"] = "other"
					} else {
						cards := []any{}
						count := maxPromoArticles + 1
						if test == "unknown-type" {
							count = 1
						}
						for i := range count {
							kind := "Complexes"
							if test == "unknown-type" {
								kind = "Unknown"
							}
							cards = append(cards, map[string]any{"article": fmt.Sprint(i), "promotionType": kind})
						}
						raw["sections"] = []any{map[string]any{"cards": cards}}
					}
					if err := json.NewEncoder(w).Encode(raw); err != nil {
						t.Errorf("encode fixture response: %v", err)
					}
					return
				}
				if strings.HasSuffix(r.URL.Path, "by-article") {
					articles++
					var raw map[string]any
					if err := json.Unmarshal(fixture(t, "promotion-article"), &raw); err != nil {
						t.Errorf("decode fixture: %v", err)
						return
					}
					raw["code"] = r.URL.Query().Get("article")
					switch test {
					case "null-price":
						raw["price"] = nil
					case "wrong-code":
						raw["code"] = "other"
					case "negative-price":
						raw["price"] = -1
					}
					if err := json.NewEncoder(w).Encode(raw); err != nil {
						t.Errorf("encode fixture response: %v", err)
					}
					return
				}
				base(w, r)
			})
			p := promotion{Slug: promoSlug, URL: promotionsURL("kazan") + promoSlug + "/"}
			got, err := c.Promotion(context.Background(), cityInfo{ID: testCityID, Slug: "kazan"}, p, false)
			if test == "null-price" {
				if err != nil || got.Offers[0].PriceFrom != nil {
					t.Fatalf("unknown price became zero: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid response accepted")
			}
			if (test == "too-many" || test == "unknown-type" || test == "wrong-page") && articles != 0 {
				t.Fatal("requested articles before validating page")
			}
		})
	}
}

func TestPromosCLI(t *testing.T) {
	p := tempPaths(t)
	if err := atomicJSON(p.config, userConfig{City: "moscow"}); err != nil {
		t.Fatal(err)
	}
	base := promoHandler(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "promotions/filtered") {
			// Keep the CLI integration independent of the clock after the fixture expires.
			var raw map[string]any
			if err := json.Unmarshal(fixture(t, "promotions"), &raw); err != nil {
				t.Errorf("decode fixture: %v", err)
				return
			}
			for _, d := range raw["docs"].([]any) {
				m := d.(map[string]any)
				m["startDate"] = nil
				m["endDate"] = nil
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(raw); err != nil {
				t.Errorf("encode fixture response: %v", err)
			}
			return
		}
		base(w, r)
	}))
	defer server.Close()
	for _, args := range [][]string{
		{"promos", "--city", "kazan"},
		{"promos", promoSlug, "--city", "kazan", "--full"},
		{"promos", promotionsURL("kazan") + promoSlug + "/"},
		{"promos", promotionsURL("moscow") + promoSlug + "/", "--city", "kazan"},
	} {
		out, err := execute(t, p, server.URL, args...)
		if err != nil || !strings.Contains(out, `"city": "kazan"`) {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	for _, args := range [][]string{{"promos", "--page", "0"}, {"promos", "--timeout=0s"}, {"promos", "a", "b"}, {"promos", "https://evil.example/ak/a/"}, {"promos", "https://www.invitro.ru/kazan/ak/../a/"}} {
		if out, err := execute(t, p, "invalid-origin", args...); err == nil || out != "" || strings.Contains(err.Error(), "invalid origin") {
			t.Fatalf("input validation %v: %v", args, err)
		}
	}
	if out, err := execute(t, p, server.URL, "promos", "missing", "--city", "kazan"); err == nil || out != "" || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("unknown promotion: %v", err)
	}
}
