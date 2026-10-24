package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSearchSellerRatings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, fields, want string
	}{
		{"card", `"rating":{"score":4.9,"summary":"1\u00a0234\u202fотзыва","userKey":"PRIVATE_CANARY"}`, `"rating":4.9,"num_ratings":1234`},
		{"nested", `"iva":{"UserInfoStep":[{"componentData":{"component":"seller-info"},"payload":{"rating":{"score":5,"summary":"12 отзывов"},"profile":{"title":"Тестовый продавец","link":"PRIVATE_CANARY"}}}]}`, `"name":"Тестовый продавец","rating":5,"num_ratings":12`},
		{"missing", `"rating":null`, `"rating":null,"num_ratings":null`},
		{"zero", `"rating":{"score":0,"summary":"Нет отзывов"}`, `"rating":0,"num_ratings":0`},
		{"rounded", `"rating":{"score":4.8,"summary":"1,2 тыс. отзывов"}`, `"rating":4.8,"num_ratings":null`},
		{"invalid", `"rating":{"score":6,"summary":"отзывы"}`, `"rating":null,"num_ratings":null`},
		{"overflow", `"rating":{"score":5,"summary":"9999999999999999999999999999 отзывов"}`, `"rating":5,"num_ratings":null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf(`{"count":1,"totalCount":1,"itemsOnPage":50,"searchCore":{"query":"телевизор","locationId":650400,"page":1},"catalog":{"items":[{"type":"item","id":1000000001,"title":"Тестовый телевизор","urlPath":"/kazan/audio_i_video/test_1000000001",%s}]}}`, test.fields)
			result, err := ParseSearch([]byte(body), webOptions())
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result.Results[0])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"seller":{`+test.want) || strings.Contains(string(encoded), "PRIVATE_CANARY") {
				t.Fatalf("wrong public seller data: %s", encoded)
			}
		})
	}
}

func TestShowSellerRatings(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, rating, want string
	}{
		{"exact count", `{"scoreFloat":4.8,"activeReviewsCount":1234,"summary":"1,2 тыс. отзывов","userKey":"PRIVATE_CANARY"}`, `"rating":4.8,"num_ratings":1234`},
		{"zero count", `{"scoreFloat":0,"activeReviewsCount":0,"summary":"Нет отзывов"}`, `"rating":0,"num_ratings":0`},
		{"missing", `null`, `"rating":null,"num_ratings":null`},
		{"negative count", `{"activeReviewsCount":-1}`, `"rating":null,"num_ratings":null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload := fmt.Sprintf(`{"loaderData":{"item":{"buyerItem":{"seller":{"name":"Тестовый продавец","avatar":"PRIVATE_CANARY"},"rating":%s,"currentUserId":"PRIVATE_CANARY","item":{"id":1000000001,"title":"Тест","url":"/kazan/audio_i_video/test_1000000001"}}}}}`, test.rating)
			quoted, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte(`<html><script>window.__staticRouterHydrationData = JSON.parse(` + string(quoted) + `);</script></html>`)
			result, err := ParseShow(body, "1000000001")
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"seller":{"name":"Тестовый продавец",`+test.want) || strings.Contains(string(encoded), "PRIVATE_CANARY") {
				t.Fatalf("wrong public seller data: %s", encoded)
			}
		})
	}
}
