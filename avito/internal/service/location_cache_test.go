package service

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCityCacheHasNoBuiltInCities(t *testing.T) {
	t.Parallel()

	cache := LocationCache{Dir: t.TempDir()}
	location, calls, err := cache.Resolve(t.Context(), nil, "Казань", RequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "lookup client must not be nil") || calls != 0 || location.ID != 0 {
		t.Fatalf("uncached city used a built-in ID: %+v, calls=%d, %v", location, calls, err)
	}
}

func TestCityCacheAvoidsLookupsAcrossInstances(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "locations")
	requests := 0
	httpClient := NewHTTPClient()
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Path != "/web/1/slocations" || req.URL.Query().Get("q") != "Москва" {
			t.Fatalf("unexpected city request: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"result":{"locations":[{"id":637640,"names":{"1":"Москва"}}]}}`)), Request: req}, nil
	})
	client, clientErr := NewClient(httpClient, testAuthStore(t))
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	location, calls, err := (LocationCache{Dir: dir}).Resolve(t.Context(), client, "Москва", RequestOptions{})
	if err != nil || calls != 1 || requests != 1 || location.ID != 637640 {
		t.Fatalf("first city lookup: %+v, calls=%d requests=%d, %v", location, calls, requests, err)
	}
	location, calls, err = (LocationCache{Dir: dir}).Resolve(t.Context(), nil, "мОСКВА", RequestOptions{})
	if err != nil || calls != 0 || requests != 1 || location.ID != 637640 || location.Name != "Москва" {
		t.Fatalf("repeated city lookup: %+v, calls=%d requests=%d, %v", location, calls, requests, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("city cache left unexpected files: %v", err)
	}
	path := filepath.Join(dir, files[0].Name())
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("incorrect city cache permissions: %v", err)
	}
	data, readErr := os.ReadFile(path) //nolint:gosec
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(data), "SYNTHETIC_AUTH") {
		t.Fatal("city cache stored authentication data")
	}
	if err := os.WriteFile(path, []byte(`{"city":"Другой город","id":123}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (LocationCache{Dir: dir}).Resolve(t.Context(), client, "Москва", RequestOptions{}); err == nil || !strings.Contains(err.Error(), "invalid city cache") || requests != 1 {
		t.Fatalf("bad cache silently used a different city or made an HTTP request: %v", err)
	}
}
