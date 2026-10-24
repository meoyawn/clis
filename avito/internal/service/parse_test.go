package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name) //nolint:gosec
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func webOptions() SearchOptions {
	return SearchOptions{Query: "телевизор", LocationID: 650400, Page: 1}
}

func TestCapturedWebSearch(t *testing.T) {
	t.Parallel()

	result, err := ParseSearch(fixture(t, "web_search.json"), webOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 50 || result.Total != 5887 || result.AccessibleTotal == nil || *result.AccessibleTotal != 1500 {
		t.Fatalf("wrong counts: %+v", result)
	}
	if result.IsAuthenticated == nil || !*result.IsAuthenticated || result.CategoryID != 32 || result.NextPage == nil || *result.NextPage != 2 {
		t.Fatal("lost captured search metadata")
	}
	first := result.Results[0]
	if first.ID != "1000000001" || first.Title != "Тестовый телевизор" || first.Price.Amount == nil || *first.Price.Amount != 13200 {
		t.Fatalf("wrong first listing: %+v", first)
	}
	if strings.Contains(first.URL, "?") || !strings.HasPrefix(first.URL, WebOrigin+"/ufa/") {
		t.Fatalf("unexpected canonical URL %q", first.URL)
	}
	if len(first.Images) != 1 {
		t.Fatalf("expected one retained fixture image, got %d", len(first.Images))
	}
}

func TestCapturedListingAndPrivateStateExclusion(t *testing.T) {
	t.Parallel()

	result, err := ParseShow(fixture(t, "web_show.html"), "1000000002")
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "Тестовый телевизор 2" || result.Price.Amount == nil || *result.Price.Amount != 13500 || result.Price.Currency != "RUB" {
		t.Fatalf("wrong details: %+v", result.Listing)
	}
	if len(result.Parameters) != 4 || result.Parameters[2].Value != "Example 32" || result.City != "Казань" {
		t.Fatalf("lost specifications: %+v", result.Listing)
	}
	if !strings.Contains(result.Description, "Ждём вас") || !strings.Contains(result.Description, "\n\n") || strings.Contains(result.Description, "<p>") {
		t.Fatal("description lost text or paragraph boundaries")
	}
	if result.IsAuthenticated == nil || !*result.IsAuthenticated || result.Active == nil || !*result.Active {
		t.Fatal("lost login/activity status")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-user@example.invalid", "SYNTHETIC_SECRET_DO_NOT_PRINT", "999999999", "currentUserId", "nonce"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("output leaked fixture canary %q", secret)
		}
	}
}

func TestWrongPageLocationAndListingFail(t *testing.T) {
	t.Parallel()

	for _, options := range []SearchOptions{
		{Query: "телевизор", LocationID: 650400, Page: 2},
		{Query: "телевизор", LocationID: 637640, Page: 1},
	} {
		if _, err := ParseSearch(fixture(t, "web_search.json"), options); err == nil {
			t.Fatal("silently accepted wrong page/location")
		}
	}
	if _, err := ParseShow(fixture(t, "web_show.html"), "12345"); err == nil {
		t.Fatal("accepted wrong listing")
	}
	if _, err := ParseShow([]byte("<html><title>Captcha</title></html>"), "12345"); err == nil {
		t.Fatal("accepted challenge as listing")
	}
}

func TestEmptySearchIsDifferentFromInvalidResponse(t *testing.T) {
	t.Parallel()

	empty := []byte(`{"count":0,"totalCount":0,"itemsOnPage":50,"catalog":{"items":[]},"searchCore":{"query":"телевизор","locationId":650400,"page":1}}`)
	result, err := ParseSearch(empty, webOptions())
	if err != nil || result.Count != 0 || result.HasMore == nil || *result.HasMore {
		t.Fatalf("empty search failed: %v, %+v", err, result)
	}
	for _, body := range []string{`null`, `{}`, `{"error":{"message":"login"}}`, `<html>captcha</html>`} {
		if _, err := ParseSearch([]byte(body), webOptions()); err == nil {
			t.Fatalf("accepted non-search response %q", body)
		}
	}
}

func TestListingReferenceValidation(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"1000000002",
		"https://www.avito.ru/kazan/audio_i_video/tv_1000000002?context=discard#fragment",
		"http://m.avito.ru/kazan/audio_i_video/tv_1000000002",
	} {
		ref, err := ParseItemReference(input)
		if err != nil || ref.ID != "1000000002" || strings.Contains(ref.URL, "?") {
			t.Fatalf("%q: %+v %v", input, ref, err)
		}
	}
	for _, input := range []string{
		"https://www.avito.ru.evil.test/kazan/audio/tv_123", "https://secret@example.test/kazan/audio/tv_123",
		"https://www.avito.ru:443/kazan/audio/tv_123", "https://www.avito.ru/../audio/tv_123",
		"https://www.avito.ru/kazan/audio/a%2Fb_123", "file:///kazan/audio/tv_123", "-1",
	} {
		_, err := ParseItemReference(input)
		if err == nil {
			t.Fatalf("accepted %q", input)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("URL credentials leaked in error")
		}
	}
}
