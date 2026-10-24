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
	"strconv"
	"strings"
	"testing"
	"time"

	avito "github.com/meoyawn/clis/avito/internal/service"
	"github.com/steipete/sweetcookie"
)

func testConfigStore(t *testing.T) avito.ConfigStore {
	t.Helper()
	return avito.ConfigStore{Path: filepath.Join(t.TempDir(), "avito", "config.json")}
}

func commandSetup(t *testing.T, transport transportFunc) setupFunc {
	t.Helper()
	auth := avito.AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	if _, err := auth.Import(t.Context(), avito.ChromeBrowser(), "", func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "session", Value: "SYNTHETIC_COOKIE", Domain: "avito.ru", Path: "/"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	httpClient := avito.NewHTTPClient()
	httpClient.Transport = transport
	client, err := avito.NewClient(httpClient, auth)
	if err != nil {
		t.Fatal(err)
	}
	index := avito.URLIndex{Dir: filepath.Join(t.TempDir(), "items")}
	locations := avito.LocationCache{Dir: filepath.Join(t.TempDir(), "locations")}
	return func() (dependencies, error) {
		return dependencies{client: client, index: index, locations: locations}, nil
	}
}

func TestConfigCommandsAreLocalAndPreserveOtherSettings(t *testing.T) {
	t.Parallel()

	store := testConfigStore(t)
	setup := func() (dependencies, error) {
		t.Fatal("local config command initialized HTTP or auth")
		return dependencies{}, nil
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"config"}, &out, io.Discard, setup, store); err != nil {
		t.Fatal(err)
	}
	var defaults avito.Config
	if err := json.Unmarshal(out.Bytes(), &defaults); err != nil || defaults != avito.DefaultConfig() {
		t.Fatalf("config did not print all defaults: %s, %v", out.String(), err)
	}
	for _, pair := range [][2]string{
		{"local-priority", "false"}, {"timeout", "2s"},
	} {
		if err := run(t.Context(), []string{"config", "set", pair[0], pair[1]}, io.Discard, io.Discard, setup, store); err != nil {
			t.Fatal(err)
		}
	}
	config, err := store.Load()
	if err != nil || config.City != "" || config.LocalPriority || config.Timeout != "2s" {
		t.Fatalf("saved config lost an update: %+v, %v", config, err)
	}
	before, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, args := range [][]string{
		{"set"}, {"set", "city"}, {"set", "city", "Казань", "extra"}, {"get", "city"},
		{"set", "unknown", "value"}, {"set", "city", ""}, {"set", "city", "Казань\n"},
		{"set", "limit", "10"}, {"set", "category-id", "32"}, {"set", "page", "2"},
		{"set", "timeout", "0s"}, {"set", "location-id", "650400"}, {"set", "local-priority", "bad"},
		{"set", "user-agent", "custom-agent"},
	} {
		if err := run(t.Context(), append([]string{"config"}, args...), io.Discard, io.Discard, setup, store); err == nil {
			t.Fatalf("accepted invalid config command %q", args)
		}
		after, readErr := os.ReadFile(store.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("invalid command %q changed saved config", args)
		}
	}
	var visible map[string]any
	if err := json.Unmarshal(before, &visible); err != nil || len(visible) != 3 {
		t.Fatalf("config persisted fields other than user preferences: %s, %v", before, err)
	}
	out.Reset()
	if err := run(t.Context(), []string{"config"}, &out, io.Discard, setup, store); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &visible); err != nil || len(visible) != 3 || visible["city"] != "" || visible["local-priority"] != false || visible["timeout"] != "2s" {
		t.Fatalf("config output exposed search parameters or lost preferences: %s, %v", out.String(), err)
	}
}

func TestConfiguredCityAndExplicitFlagsReachAvito(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		args         []string
		id           int64
		count, calls int
		overrideAll  bool
	}{
		{"saved city", []string{"search", "кокосовое масло"}, 650400, 10, 1, false},
		{"city after query", []string{"search", "кокосовое масло", "--city", "Москва"}, 637640, 10, 2, false},
		{"city before query", []string{"search", "--city=кАзАнЬ", "кокосовое масло"}, 650400, 10, 1, false},
		{"numeric location override", []string{"search", "кокосовое масло", "--location-id", "637640"}, 637640, 10, 1, false},
		{"all flag overrides", []string{"search", "кокосовое масло", "--city", "Москва", "--local-priority=false", "--limit=0", "--category-id=32", "--page=2", "--timeout=1s", "--user-agent=flag-agent"}, 637640, 50, 2, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := testConfigStore(t)
			config := avito.DefaultConfig()
			config.City, config.Timeout = "Москва", "2s"
			if err := store.Save(config); err != nil {
				t.Fatal(err)
			}
			locations, err := os.ReadFile("../service/testdata/web_locations.json")
			if err != nil {
				t.Fatal(err)
			}
			searchFixture, err := os.ReadFile("../service/testdata/web_search.json")
			if err != nil {
				t.Fatal(err)
			}
			requests, lookups := 0, 0
			wantID, wantPage, wantCategory, wantPriority := int64(650400), 1, int64(0), "1"
			wantAgent, wantTimeout := avito.DefaultWebUserAgent, 2*time.Second
			setup := commandSetup(t, func(req *http.Request) (*http.Response, error) {
				requests++
				if req.Header.Get("Cookie") != "session=SYNTHETIC_COOKIE" || req.Header.Get("User-Agent") != wantAgent {
					t.Fatal("request lost saved cookies or the effective user-agent")
				}
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > wantTimeout || time.Until(deadline) <= 0 {
					t.Fatal("request lost the effective timeout")
				}
				var body []byte
				query := req.URL.Query()
				switch req.URL.Path {
				case "/web/1/slocations":
					lookups++
					switch query.Get("q") {
					case "кАзАнЬ", "Казань":
						body = locations
					case "Москва":
						body = []byte(`{"result":{"locations":[{"id":637640,"names":{"1":"Москва"}}]}}`)
					default:
						t.Fatalf("unexpected city query %q", query.Get("q"))
					}
				case "/web/1/js/items":
					if query.Get("name") != "кокосовое масло" || query.Get("locationId") != strconv.FormatInt(wantID, 10) || query.Get("localPriority") != wantPriority {
						t.Fatalf("city or priority was not sent to Avito: %s", req.URL)
					}
					page, category := "", ""
					if wantPage > 1 {
						page = strconv.Itoa(wantPage)
					}
					if wantCategory > 0 {
						category = strconv.FormatInt(wantCategory, 10)
					}
					if query.Get("p") != page || query.Get("categoryId") != category {
						t.Fatalf("search ignored config or explicit flags: %s", req.URL)
					}
					var payload map[string]any
					if err := json.Unmarshal(searchFixture, &payload); err != nil {
						t.Fatal(err)
					}
					payload["searchCore"] = map[string]any{"query": "кокосовое масло", "locationId": wantID, "categoryId": wantCategory, "page": wantPage}
					body, err = json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unexpected request %s", req.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
			})
			if err := run(t.Context(), []string{"config", "set", "city", "Казань"}, io.Discard, io.Discard, setup, store); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load()
			if err != nil || loaded.City != "Казань" || loaded.Timeout != "2s" || lookups != 1 || requests != 1 {
				t.Fatalf("city preference was not resolved once and saved: %+v, %v", loaded, err)
			}
			saved, readErr := os.ReadFile(store.Path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantID = test.id
			if test.overrideAll {
				wantPage, wantCategory, wantPriority, wantAgent, wantTimeout = 2, 32, "0", "flag-agent", time.Second
			}
			before := requests
			var out bytes.Buffer
			if err := run(t.Context(), test.args, &out, io.Discard, setup, avito.ConfigStore{Path: store.Path}); err != nil {
				t.Fatal(err)
			}
			var result avito.SearchResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.LocationID != test.id || result.Count != test.count || len(result.Results) != test.count || result.HTTPRequests != test.calls || requests-before != test.calls {
				t.Fatalf("incorrect result: location=%d count=%d calls=%d reported=%d", result.LocationID, result.Count, requests-before, result.HTTPRequests)
			}
			// The fixture starts with Ufa. City preference must be sent to Avito,
			// not implemented by filtering or reordering returned listings.
			if result.Results[0].ID != "1000000001" || !strings.Contains(result.Results[0].URL, "/ufa/") {
				t.Fatal("CLI filtered or reordered the server results by city")
			}
			after, readErr := os.ReadFile(store.Path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(saved, after) {
				t.Fatal("a one-off flag changed persistent config")
			}
		})
	}
}

func TestConfigSetCityResolvesOnceAndSearchReusesInternalID(t *testing.T) {
	t.Parallel()

	store := testConfigStore(t)
	requests, lookups := 0, 0
	wantPriority := "1"
	setup := commandSetup(t, func(req *http.Request) (*http.Response, error) {
		requests++
		var body string
		switch req.URL.Path {
		case "/web/1/slocations":
			lookups++
			if req.URL.Query().Get("q") != "Москва" {
				t.Fatalf("unexpected city lookup %s", req.URL)
			}
			body = `{"result":{"locations":[{"id":637640,"names":{"1":"Москва"}}]}}`
		case "/web/1/js/items":
			if req.URL.Query().Get("locationId") != "637640" || req.URL.Query().Get("localPriority") != wantPriority {
				t.Fatalf("search ignored the cached city or priority preference: %s", req.URL)
			}
			body = `{"count":0,"totalCount":0,"itemsOnPage":50,"catalog":{"items":[]},"searchCore":{"query":"tv","locationId":637640,"page":1}}`
		default:
			t.Fatalf("unexpected request %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})
	var out bytes.Buffer
	if err := run(t.Context(), []string{"config", "set", "city", "Москва"}, &out, io.Discard, setup, store); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 || requests != 1 {
		t.Fatalf("setting an unknown city made %d requests and %d lookups", requests, lookups)
	}
	saved, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, data := range [][]byte{out.Bytes(), saved} {
		var values map[string]any
		if err := json.Unmarshal(data, &values); err != nil || len(values) != 3 || values["city"] != "Москва" || bytes.Contains(data, []byte("637640")) || bytes.Contains(data, []byte("location-id")) {
			t.Fatalf("config exposed internal IDs: %s, %v", data, err)
		}
	}
	for i := range 3 {
		out.Reset()
		if err := run(t.Context(), []string{"search", "tv"}, &out, io.Discard, setup, avito.ConfigStore{Path: store.Path}); err != nil {
			t.Fatal(err)
		}
		var result avito.SearchResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.HTTPRequests != 1 || result.LocationID != 637640 || requests != i+2 || lookups != 1 {
			t.Fatalf("search repeated a city lookup: requests=%d lookups=%d, %v", requests, lookups, err)
		}
		if i == 0 {
			if err := run(t.Context(), []string{"config", "set", "local-priority", "false"}, io.Discard, io.Discard, setup, store); err != nil {
				t.Fatal(err)
			}
			wantPriority = "0"
		}
	}
	if err := run(t.Context(), []string{"config", "set", "city", "москва"}, io.Discard, io.Discard, setup, store); err != nil || requests != 4 {
		t.Fatalf("setting a cached city repeated a lookup: requests=%d, %v", requests, err)
	}
}

func TestFailedCityLookupPreservesConfigAndDoesNotSearch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, body, want string
		status           int
	}{
		{"unknown", `{"result":{"locations":[]}}`, "not found", 200},
		{"ambiguous", `{"result":{"locations":[{"id":1,"names":{"1":"Пример"}},{"id":2,"names":{"1":"Пример"}}]}}`, "ambiguous", 200},
		{"rate limited", `{"error":{"message":"slow down"}}`, "Retry-After: 90", 429},
		{"challenge", `<html>captcha</html>`, "decode city", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := testConfigStore(t)
			if err := store.Save(avito.DefaultConfig()); err != nil {
				t.Fatal(err)
			}
			before, readErr := os.ReadFile(store.Path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			requests := 0
			setup := commandSetup(t, func(req *http.Request) (*http.Response, error) {
				requests++
				if req.URL.Path != "/web/1/slocations" {
					t.Fatal("failed city lookup continued to search")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Retry-After": {"90"}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: req}, nil
			})
			for i, args := range [][]string{{"config", "set", "city", "Пример"}, {"search", "tv", "--city", "Пример"}, {"search", "--city", "Пример", "tv"}} {
				var out bytes.Buffer
				err := run(t.Context(), args, &out, io.Discard, setup, store)
				if err == nil || !strings.Contains(err.Error(), test.want) || requests != i+1 || out.Len() != 0 {
					t.Fatalf("incorrect lookup failure: %v, requests=%d, output=%s", err, requests, out.String())
				}
				after, readErr := os.ReadFile(store.Path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("failed city lookup replaced config")
				}
			}
		})
	}
}

func TestCommonConfigAppliesToAuthAndShow(t *testing.T) {
	t.Parallel()

	store := testConfigStore(t)
	config := avito.DefaultConfig()
	config.Timeout = "2s"
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	setup := commandSetup(t, func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if req.Header.Get("User-Agent") != avito.DefaultWebUserAgent || !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("auth/show ignored common request config")
		}
		file := "web_show.html"
		if req.URL.Path == "/web/1/profiles/list" {
			file = "web_profiles.json"
		}
		body, err := os.ReadFile("../service/testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	for _, args := range [][]string{{"auth", "status"}, {"show", "https://www.avito.ru/kazan/audio_i_video/test_tv_1000000002"}} {
		if err := run(t.Context(), args, io.Discard, io.Discard, setup, store); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBadConfigAndMissingAuthDoNotMakeRequests(t *testing.T) {
	t.Parallel()

	store := testConfigStore(t)
	config := avito.DefaultConfig()
	config.City = "Казань"
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	before, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	httpClient := avito.NewHTTPClient()
	httpClient.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("missing auth made a request")
		return nil, nil
	})
	client, clientErr := avito.NewClient(httpClient, avito.AuthStore{Path: filepath.Join(t.TempDir(), "missing.json")})
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	locations := avito.LocationCache{Dir: filepath.Join(t.TempDir(), "locations")}
	setup := func() (dependencies, error) { return dependencies{client: client, locations: locations}, nil }
	for _, args := range [][]string{{"search", "tv"}, {"search", "tv", "--city", "Казань"}, {"config", "set", "city", "Москва"}} {
		err := run(t.Context(), args, io.Discard, io.Discard, setup, store)
		if err == nil || !strings.Contains(err.Error(), "no saved Avito cookies") {
			t.Fatalf("missing session: %v", err)
		}
	}
	after, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("missing auth changed config")
	}
	if err := os.WriteFile(store.Path, []byte(`{"timeout":"0s"}`), 0600); err != nil {
		t.Fatal(err)
	}
	setup = func() (dependencies, error) {
		t.Fatal("bad config initialized HTTP")
		return dependencies{}, errors.New("unexpected setup")
	}
	if err := run(t.Context(), []string{"search", "tv"}, io.Discard, io.Discard, setup, store); err == nil {
		t.Fatal("bad config silently used default city")
	}
	if err := run(t.Context(), []string{"search", "--help"}, io.Discard, io.Discard, setup, store); err != nil {
		t.Fatalf("bad config blocked help: %v", err)
	}
}

func TestSearchWithoutACityFailsLocally(t *testing.T) {
	t.Parallel()

	setup := func() (dependencies, error) {
		t.Fatal("search without a city initialized HTTP")
		return dependencies{}, nil
	}
	err := run(t.Context(), []string{"search", "tv"}, io.Discard, io.Discard, setup, testConfigStore(t))
	if err == nil || !strings.Contains(err.Error(), "avito config set city") {
		t.Fatalf("missing city did not explain how to configure it: %v", err)
	}
}
