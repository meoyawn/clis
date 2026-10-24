package service

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResolveCityUsesCapturedLookup(t *testing.T) {
	t.Parallel()

	requests := 0
	httpClient := NewHTTPClient()
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		query := req.URL.Query()
		if req.URL.Path != "/web/1/slocations" || len(query) != 2 || query.Get("q") != "Казань" || query.Has("locationId") || query.Get("limit") != "10" {
			t.Fatalf("unexpected city request: %s", req.URL)
		}
		if req.Method != http.MethodGet || req.Header.Get("Cookie") != "session=SYNTHETIC_AUTH" || req.Header.Get("X-Source") != "client-browser" || req.Header.Get("User-Agent") != "city-test-agent" {
			t.Fatal("city lookup did not use the authenticated JSON request transport")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(fixture(t, "web_locations.json"))), Request: req}, nil
	})
	client, clientErr := NewClient(httpClient, testAuthStore(t))
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	location, err := client.ResolveCity(t.Context(), " Казань ", RequestOptions{UserAgent: "city-test-agent"})
	if err != nil || location.ID != 650400 || location.Name != "Казань" || requests != 1 {
		t.Fatalf("city lookup: %+v, %v, requests=%d", location, err, requests)
	}
}

func TestCityLookupRejectsUnknownAmbiguousAndInvalidResponses(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, city, body, want string
	}{
		{"empty matches", "Казань", `{"result":{"locations":[]}}`, "not found"},
		{"partial name", "Каз", string(fixture(t, "web_locations.json")), "not found"},
		{"unknown name", "Несуществующий город", string(fixture(t, "web_locations.json")), "not found"},
		{"ambiguous", "Пример", `{"result":{"locations":[{"id":123,"names":{"1":"Пример"},"parent":{"names":{"1":"Область А"}}},{"id":456,"names":{"1":"Пример"},"parent":{"names":{"1":"Область Б"}}}]}}`, "ambiguous"},
		{"invalid ID", "Казань", `{"result":{"locations":[{"id":0,"names":{"1":"Казань"}}]}}`, "invalid location"},
		{"missing name", "Казань", `{"result":{"locations":[{"id":650400}]}}`, "invalid location"},
		{"HTML challenge", "Казань", `<html>captcha</html>`, "decode city"},
		{"missing list", "Казань", `{"result":{}}`, "no result.locations"},
		{"null", "Казань", `null`, "no result.locations"},
		{"unexpected JSON", "Казань", `{"error":{"message":"login"}}`, "no result.locations"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseCity([]byte(test.body), test.city)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected lookup error: %v", err)
			}
		})
	}
	location, err := parseCity(fixture(t, "web_locations.json"), "кАзАнЬ")
	if err != nil || location.ID != 650400 || location.Name != "Казань" {
		t.Fatalf("case-insensitive exact name lookup: %+v, %v", location, err)
	}
}

func TestCityLookupInvalidInputAndMissingSessionMakeNoRequests(t *testing.T) {
	t.Parallel()

	httpClient := NewHTTPClient()
	httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid city or missing credentials made a request")
		return nil, nil
	})
	client, clientErr := NewClient(httpClient, AuthStore{})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	for _, city := range []string{"", " ", "Казань\n", strings.Repeat("a", 257), "Казань"} {
		if _, err := client.ResolveCity(t.Context(), city, RequestOptions{}); err == nil {
			t.Fatalf("accepted city %q without credentials", city)
		}
	}
}

func TestSearchURLSendsCityAndPriority(t *testing.T) {
	t.Parallel()

	for _, priority := range []bool{false, true} {
		options := SearchOptions{Query: "кокосовое масло", LocationID: 650400, LocalPriority: priority, Page: 1}
		target, err := SearchURL(options)
		if err != nil {
			t.Fatal(err)
		}
		want := "0"
		if priority {
			want = "1"
		}
		query := target.Query()
		if query.Get("name") != "кокосовое масло" || query.Get("locationId") != "650400" || query.Get("localPriority") != want {
			t.Fatalf("incorrect search parameters: %s", target)
		}
	}
}
