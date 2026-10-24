package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	avito "github.com/meoyawn/clis/avito/internal/service"
	"github.com/steipete/sweetcookie"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRecordedSearchThenShowCommands(t *testing.T) {
	t.Parallel()

	searchCases := []struct {
		name  string
		args  []string
		count int
		calls int
	}{
		{"full page", []string{"search", "телевизор", "--location-id", "650400", "--limit=0"}, 50, 1},
		{"default limit and first city lookup", []string{"search", "телевизор"}, 10, 2},
		{"limit before query", []string{"search", "--limit", "1", "телевизор"}, 1, 2},
		{"limit after query", []string{"search", "телевизор", "--limit", "10"}, 10, 2},
		{"limit equals page size", []string{"search", "телевизор", "--limit=50"}, 50, 2},
		{"limit exceeds page size", []string{"search", "телевизор", "--limit", "100"}, 50, 2},
		{"zero limit", []string{"search", "телевизор", "--limit", "0"}, 50, 2},
	}
	for _, tc := range searchCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			testRecordedCommands(t, tc.args, tc.count, tc.calls)
		})
	}
}

func testRecordedCommands(t *testing.T, searchArgs []string, count, calls int) {
	t.Helper()

	requests, imports := 0, 0
	index := avito.URLIndex{Dir: filepath.Join(t.TempDir(), "items")}
	locations := avito.LocationCache{Dir: filepath.Join(t.TempDir(), "locations")}
	configStore := testConfigStore(t)
	config := avito.DefaultConfig()
	config.City = "Казань"
	if err := configStore.Save(config); err != nil {
		t.Fatal(err)
	}
	auth := avito.AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	httpClient := avito.NewHTTPClient()
	httpClient.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Host != "www.avito.ru" || req.Header.Get("Cookie") != "session=SYNTHETIC_COOKIE" {
			t.Fatal("unexpected origin or cookie header")
		}
		name, mime := "web_show.html", "text/html"
		switch req.URL.Path {
		case "/web/1/profiles/list":
			name, mime = "web_profiles.json", "application/json"
		case "/web/1/slocations":
			name, mime = "web_locations.json", "application/json"
		case "/web/1/js/items":
			name, mime = "web_search.json", "application/json"
			if req.URL.Query().Get("name") != "телевизор" || req.URL.Query().Get("locationId") != "650400" || req.URL.Query().Get("localPriority") != "1" || req.Header.Get("X-Source") != "client-browser" {
				t.Fatal("wrong search request")
			}
		case "/kazan/audio_i_video/test_tv_1000000002":
		default:
			t.Fatalf("unexpected request path %s", req.URL.Path)
		}
		body, err := os.ReadFile("../service/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {mime}}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	client, clientErr := avito.NewClient(httpClient, auth)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	readCookies := func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		imports++
		if len(opts.Browsers) != 1 || opts.Browsers[0] != sweetcookie.BrowserFirefox || opts.Profiles[sweetcookie.BrowserFirefox] != "Profile 1" {
			t.Fatalf("wrong browser options: %+v", opts)
		}
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "session", Value: "SYNTHETIC_COOKIE", Domain: "avito.ru", Path: "/"}}}, nil
	}
	setup := func() (dependencies, error) {
		return dependencies{client: client, index: index, locations: locations, readCookies: readCookies}, nil
	}
	var out, stderr bytes.Buffer
	for i, args := range [][]string{{"auth", "import", "--browser", "firefox", "--browser-profile", "Profile 1", "--json"}, {"auth", "status", "--json"}} {
		out.Reset()
		if err := run(t.Context(), args, &out, &stderr, setup, testConfigStore(t)); err != nil {
			t.Fatal(err)
		}
		var status avito.AuthStatus
		if err := json.Unmarshal(out.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if !status.Imported || status.UsableCookieCount != 1 || status.Browser != "firefox" || !status.LoginVerified || status.IsAuthenticated == nil || !*status.IsAuthenticated || status.Account == nil || status.Account.Name != "Алексей Пример" || status.HTTPRequests != 1 || requests != i+1 || imports != 1 || strings.Contains(out.String(), "SYNTHETIC_COOKIE") {
			t.Fatalf("invalid auth output: %s", out.String())
		}
	}
	out.Reset()
	before := requests
	if err := run(t.Context(), searchArgs, &out, &stderr, setup, configStore); err != nil {
		t.Fatal(err)
	}
	var search avito.SearchResult
	if err := json.Unmarshal(out.Bytes(), &search); err != nil {
		t.Fatal(err)
	}
	if search.Count != count || len(search.Results) != count || search.Results[0].ID != "1000000001" {
		t.Fatalf("incorrect search results: count=%d listings=%d, want %d starting with listing 1000000001", search.Count, len(search.Results), count)
	}
	if search.HTTPRequests != calls || requests-before != calls {
		t.Fatalf("search made %d requests, reported %d; want %d", requests-before, search.HTTPRequests, calls)
	}
	if search.Total != 5887 || search.AccessibleTotal == nil || *search.AccessibleTotal != 1500 || search.Page != 1 || search.HasMore == nil || !*search.HasMore || search.NextPage == nil || *search.NextPage != 2 {
		t.Fatal("lost server totals or pagination metadata")
	}
	for range 2 {
		out.Reset()
		if err := run(t.Context(), []string{"show", "1000000002"}, &out, &stderr, setup, testConfigStore(t)); err != nil {
			t.Fatal(err)
		}
		var show avito.ShowResult
		if err := json.Unmarshal(out.Bytes(), &show); err != nil {
			t.Fatal(err)
		}
		if show.ID != "1000000002" || show.HTTPRequests != 1 || show.Price.Amount == nil || *show.Price.Amount != 13500 {
			t.Fatal("wrong show output")
		}
		for _, secret := range []string{"SYNTHETIC_COOKIE", "SYNTHETIC_SECRET_DO_NOT_PRINT", "fixture-user@example.invalid"} {
			if strings.Contains(out.String(), secret) || strings.Contains(stderr.String(), secret) {
				t.Fatalf("leaked %q", secret)
			}
		}
	}
	if requests != calls+4 || imports != 1 {
		t.Fatalf("expected one explicit import, two auth checks, one search, and two shows, got requests=%d imports=%d", requests, imports)
	}
}

func Test429ReturnsErrorWithoutOutputOrExtraRequests(t *testing.T) {
	t.Parallel()

	requests := 0
	httpClient := avito.NewHTTPClient()
	httpClient.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"120"}}, Body: io.NopCloser(strings.NewReader(`{"too-many-requests":{"message":"IP restricted"}}`)), Request: req}, nil
	})
	auth := avito.AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	if _, err := auth.Import(t.Context(), avito.ChromeBrowser(), "", func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "session", Value: "SYNTHETIC_COOKIE", Domain: "avito.ru", Path: "/"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	client, clientErr := avito.NewClient(httpClient, auth)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	index := avito.URLIndex{Dir: t.TempDir()}
	setup := func() (dependencies, error) { return dependencies{client: client, index: index}, nil }
	var out, stderr bytes.Buffer
	err := run(t.Context(), []string{"search", "tv", "--location-id", "650400"}, &out, &stderr, setup, testConfigStore(t))
	var failure *avito.HTTPError
	if !errors.As(err, &failure) || failure.Status != 429 || failure.RetryAfter != "120" || requests != 1 || out.Len() != 0 {
		t.Fatalf("unexpected failure behavior: %v requests=%d stdout=%q", err, requests, out.String())
	}
	files, readErr := os.ReadDir(index.Dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(files) != 0 {
		t.Fatal("429 created persistent policy state")
	}
}

func TestHelpAndInvalidArgumentsDoNotInitializeIO(t *testing.T) {
	t.Parallel()

	setup := func() (dependencies, error) {
		t.Fatal("unexpected IO initialization")
		return dependencies{}, nil
	}
	for _, args := range [][]string{{"--help"}, {"search", "--help"}, {"show", "--help"}, {"auth", "--help"}, {"auth", "import", "--help"}, {"auth", "status", "--help"}, {"config", "--help"}} {
		if err := run(t.Context(), args, io.Discard, io.Discard, setup, testConfigStore(t)); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"search"}, {"show"}, {"search", "tv", "--page", "0"},
		{"search", "tv", "--limit", "-1"}, {"search", "tv", "--limit", "invalid"}, {"search", "tv", "--limit"},
		{"search", "tv", "--city"}, {"search", "tv", "--city", ""}, {"search", "tv", "--city", "  "}, {"search", "tv", "--city", "Казань", "--location-id", "637640"}, {"search", "tv", "--local-priority=invalid"}, {"show", "12345", "--city", "Казань"},
		{"search", "tv", "--source", "bad"}, {"show", "https://outside.test/item_123"}, {"search", "tv", "--timeout", "0s"}, {"auth"}, {"auth", "import"}, {"auth", "import", "--browser", "none"}, {"auth", "status", "extra"}, {"auth", "import", "--browser", "chrome", "--timeout", "0s"}, {"auth", "status", "--timeout", "0s"}, {"auth", "status", "--user-agent", "bad\r\nheader"}, {"auth", "status", "--json=invalid"},
	} {
		if err := run(t.Context(), args, io.Discard, io.Discard, setup, testConfigStore(t)); err == nil {
			t.Fatalf("accepted invalid arguments %q", args)
		}
	}
}

func TestUnknownIDFailsBeforeCookieOrHTTPAccess(t *testing.T) {
	t.Parallel()

	httpClient := avito.NewHTTPClient()
	httpClient.Transport = transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected HTTP request"); return nil, nil })
	client, clientErr := avito.NewClient(httpClient, avito.AuthStore{})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	setup := func() (dependencies, error) {
		return dependencies{client: client, index: avito.URLIndex{Dir: t.TempDir()}}, nil
	}
	err := run(t.Context(), []string{"show", "12345"}, io.Discard, io.Discard, setup, testConfigStore(t))
	if err == nil || !strings.Contains(err.Error(), "full Avito URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}
