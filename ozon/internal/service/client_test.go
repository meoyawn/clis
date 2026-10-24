package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body []byte, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	// #nosec G304 -- Fixture names are supplied only by these tests.
	b, e := os.ReadFile(filepath.Join("testdata", name+".json"))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func fixturePage(t *testing.T, name string) *page {
	t.Helper()
	var p page
	if e := json.Unmarshal(fixture(t, name), &p); e != nil {
		t.Fatal(e)
	}
	return &p
}
func testStore(t *testing.T) AuthStore {
	t.Helper()
	store := AuthStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	r := authRecord{Version: 1, Browser: "chrome", ImportedAt: time.Now(), Cookies: []storedCookie{{Name: "session", Value: "secret-cookie-value", Domain: ".ozon.ru", Path: "/", Secure: true}}}
	if e := store.save(t.Context(), &r); e != nil {
		t.Fatal(e)
	}
	return store
}
func testClient(t *testing.T, fn roundTripFunc) *Client {
	t.Helper()
	c, e := NewClient(&http.Client{Transport: fn}, testStore(t))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func requireRequest(t *testing.T, r *http.Request) string {
	t.Helper()
	if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "www.ozon.ru" || r.URL.Path != entrypointPath {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
	}
	if r.Header.Get("Cookie") != "session=secret-cookie-value" {
		t.Fatal("saved cookie not forwarded to Ozon")
	}
	if r.Header.Get("User-Agent") == "" || r.Header.Get("X-O3-App-Name") != "dweb_client" {
		t.Fatal("missing browser headers")
	}
	return r.URL.Query().Get("url")
}

func TestRequestCountsDoNotGrowWithResults(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Parallel()
			main := fixturePage(t, "product")
			aspects, err := main.first("webAspects")
			if err != nil {
				t.Fatal(err)
			}
			variants := obj(arr(at(aspects, "aspects"))[0])
			sample := arr(variants["variants"])[0]
			many := make([]any, n)
			for i := range many {
				many[i] = sample
			}
			variants["variants"] = many
			putWidget(t, main, "webAspects-123-default-1", aspects)
			offers := fixturePage(t, "sellers")
			list, err := offers.first("webSellerList")
			if err != nil {
				t.Fatal(err)
			}
			manyOffers := make([]any, n)
			for i := range manyOffers {
				manyOffers[i] = object{"id": fmt.Sprint(i + 100), "sku": fmt.Sprint(i + 200), "name": "Seller", "price": object{"cardPrice": object{"price": "200 ₽"}}}
			}
			list["sellers"] = manyOffers
			putWidget(t, offers, "webSellerList-123-default-1", list)
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				path := requireRequest(t, r)
				var body []byte
				var err error
				switch {
				case path == "/product/100/":
					body, err = json.Marshal(main)
				case strings.Contains(path, "layout_container=pdpPage2column"):
					body = fixture(t, "details")
				case strings.HasPrefix(path, "/modal/otherOffersFromSellers?"):
					body, err = json.Marshal(offers)
				default:
					t.Fatalf("unexpected path %s", path)
				}
				if err != nil {
					t.Fatal(err)
				}
				return response(body, 200), nil
			})
			got, err := c.Show(t.Context(), ItemReference{SKU: "100", Path: "/product/100/"}, ShowOptions{Full: true}, RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 4 || got.HTTPRequests != 4 || !got.Full {
				t.Fatalf("%d offers used %d requests, metadata %d", n, calls, got.HTTPRequests)
			}
			if len(got.Sellers) != n+1 || len(got.Variants[0].Variants) != n {
				t.Fatal("offers or variants lost")
			}
		})
	}
	for _, name := range []string{"search", "filters", "reviews"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				requireRequest(t, r)
				return response(fixture(t, name), 200), nil
			})
			var err error
			switch name {
			case "search":
				_, err = c.Search(t.Context(), SearchOptions{Query: "test", Page: 1}, RequestOptions{})
			case "filters":
				_, err = c.Filters(t.Context(), SearchOptions{Query: "test", Page: 1, Filters: url.Values{"delivery": {"4"}}}, RequestOptions{})
			case "reviews":
				_, err = c.Reviews(t.Context(), ItemReference{SKU: "100", Path: "/product/100/"}, ReviewsOptions{Page: 1, Scope: SKUReviewScope(), Sort: UsefulReviewSort()}, RequestOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("made %d requests", calls)
			}
		})
	}
}

func TestNoExternalRedirectsRetriesOrErrorBodyLeaks(t *testing.T) {
	t.Parallel()
	for _, status := range []int{302, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				resp := response([]byte("secret-response-body"), status)
				resp.Header.Set("Location", "https://example.invalid/steal")
				resp.Header.Set("Retry-After", "Wed, 09 Sep 2026 12:00:00 GMT")
				return resp, nil
			})
			_, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1}, RequestOptions{})
			if err == nil || calls != 1 {
				t.Fatalf("requests=%d err=%v", calls, err)
			}
			if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "Retry-After: Wed, 09 Sep 2026 12:00:00 GMT") {
				t.Fatalf("unsafe or incomplete error: %v", err)
			}
			if strings.Contains(err.Error(), "product URL") {
				t.Fatalf("product advice for a search failure: %v", err)
			}
		})
	}
}

func TestSearchAndFiltersFollowCategoryPredictionRedirect(t *testing.T) {
	t.Parallel()
	const query = "кокосовое масло aroy d"
	for _, command := range []string{"search", "filters"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			calls := 0
			var category, cursor string
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				path := requireRequest(t, r)
				u, err := url.Parse(path)
				if err != nil {
					t.Fatal(err)
				}
				if calls == 1 {
					q := u.Query()
					if u.Path != "/search/" || q.Has("deny_category_prediction") {
						t.Fatal("category prediction disabled before Ozon's redirect")
					}
					q.Set("brand_was_predicted", "true")
					q.Set("brand", "82077144")
					q.Set("category_was_predicted", "true")
					q.Set("deny_category_prediction", "true")
					category = "/category/coconut-oil-100/aroy-d-200/?" + q.Encode()
					resp := response(nil, http.StatusFound)
					resp.Header.Set("Location", entrypointPath+"?"+url.Values{"url": {category}, "page_changed": {"true"}}.Encode())
					return resp, nil
				}
				if calls == 2 && path != category || calls == 3 && path != cursor || calls > 3 {
					t.Fatalf("unexpected request %d: %s", calls, path)
				}
				p := fixturePage(t, command)
				p.PageInfo.URL = path
				p.PageInfo.PageType = "category"
				if u.Query().Get("brand") != "82077144" {
					t.Fatal("predicted brand was lost during navigation")
				}
				active, err := p.first("searchResultsFiltersActive")
				if err != nil {
					t.Fatal(err)
				}
				if active == nil {
					active = object{}
				}
				active["activeFilters"] = append(arr(active["activeFilters"]), object{"key": "brand", "activeValues": []any{object{"cellTrackingInfo": object{"filterValue": "82077144"}}}})
				putWidget(t, p, "searchResultsFiltersActive-123-default-1", active)
				loggedIn := true
				p.UserInfo.User.LoggedIn = &loggedIn
				next := u.Query()
				next.Set("page", "2")
				next.Set("layout_page_index", "2")
				next.Set("paginator_token", "opaque")
				p.NextPage = u.Path + "?" + next.Encode()
				if calls == 3 {
					g, err := p.first("tileGridDesktop")
					if err != nil {
						t.Fatal(err)
					}
					g["page"] = 2
					putWidget(t, p, "tileGridDesktop-123-default-1", g)
					p.Layout = []layoutWidget{{Component: "tileGridDesktop", Name: "catalog.searchResultsV2", StateID: "tileGridDesktop-123-default-1"}}
					p.NextPage = ""
				}
				b, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(b, http.StatusOK), nil
			})
			o := SearchOptions{Query: query, Page: 1}
			var meta Metadata
			if command == "search" {
				result, err := c.Search(t.Context(), o, RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if result.Query != query || result.Count != 1 || !result.HasMore {
					t.Fatalf("unexpected search result: %+v", result)
				}
				cursor = result.NextCursor
				meta = result.Metadata
			} else {
				o.Filters = url.Values{"delivery": {"4"}}
				result, err := c.Filters(t.Context(), o, RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if result.Query != query || len(result.Filters) == 0 {
					t.Fatalf("unexpected filters result: %+v", result)
				}
				meta = result.Metadata
			}
			if calls != 2 || meta.HTTPRequests != 2 || len(meta.SourceURLs) != 2 || meta.SourceURLs[1] != WebOrigin+category || meta.IsAuthenticated == nil || !*meta.IsAuthenticated || len(meta.Warnings) != 1 || !strings.Contains(meta.Warnings[0], "brand=82077144") {
				t.Fatalf("requests=%d metadata=%d", calls, meta.HTTPRequests)
			}
			if command == "search" {
				result, err := c.Search(t.Context(), SearchOptions{Query: query, Page: 1, Cursor: cursor}, RequestOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if calls != 3 || result.HTTPRequests != 1 || result.Page != 2 || result.Count != 1 || result.HasMore {
					t.Fatalf("unexpected category continuation: calls=%d result=%+v", calls, result)
				}
			}
		})
	}
}

func TestUntrustedInputMakesZeroRequests(t *testing.T) {
	t.Parallel()
	calls := 0
	c := testClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		t.Fatal("network request for invalid input")
		return nil, nil
	})
	for _, cursor := range []string{"https://evil.example/search/?text=test&page=2", "//evil.example/search/?text=test&page=2", "/search/?text=another&page=2", "/my/main", "/search/?text=test&page=0"} {
		if _, e := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, Cursor: cursor}, RequestOptions{}); e == nil {
			t.Errorf("accepted cursor %s", cursor)
		}
	}
	for _, cursor := range []string{"/product/101/reviews/?page=2", "/my/main", "https://evil.example/product/100/reviews/?page=2"} {
		_, e := c.Reviews(t.Context(), ItemReference{SKU: "100", Path: "/product/100/"}, ReviewsOptions{Page: 1, Cursor: cursor, Scope: SKUReviewScope(), Sort: UsefulReviewSort()}, RequestOptions{})
		if e == nil {
			t.Errorf("accepted cursor %s", cursor)
		}
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestMissingCredentialsMakeZeroRequests(t *testing.T) {
	t.Parallel()
	c, e := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network without cookies"); return nil, nil })}, AuthStore{Path: filepath.Join(t.TempDir(), "absent")})
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Search(t.Context(), SearchOptions{Query: "test", Page: 1}, RequestOptions{})
	if e == nil {
		t.Fatal("accepted absent session")
	}
	s, e := c.AuthStatus(t.Context(), RequestOptions{})
	if e == nil || s.HTTPRequests != 0 {
		t.Fatalf("status=%+v error=%v", s, e)
	}
}

func TestSearchCursorIsUsedVerbatim(t *testing.T) {
	t.Parallel()
	first, e := parseSearch(fixturePage(t, "search"), SearchOptions{Query: "test", Page: 1}, "/search/?text=test")
	if e != nil {
		t.Fatal(e)
	}
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		path := requireRequest(t, r)
		if path != first.NextCursor {
			t.Fatalf("cursor changed: %s", path)
		}
		p := fixturePage(t, "search")
		w, err := p.first("tileGridDesktop")
		if err != nil {
			t.Fatal(err)
		}
		w["page"] = 2
		p.WidgetStates = map[string]json.RawMessage{}
		putWidget(t, p, "tileGridDesktop-999-default-2", w)
		p.Layout = []layoutWidget{{Component: "tileGridDesktop", Name: "catalog.searchResultsV2", StateID: "tileGridDesktop-999-default-2"}}
		p.PageInfo.URL = path
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return response(b, 200), nil
	})
	got, e := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, Cursor: first.NextCursor}, RequestOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if got.Page != 2 || got.HasMore || got.NextPage != nil {
		t.Fatalf("incorrect final page: %+v", got.Pagination)
	}
}

func TestSearchEscapesQueryAndFilters(t *testing.T) {
	t.Parallel()
	path, e := SearchPath(SearchOptions{Query: "USB C & size=1", Page: 1, Filters: url.Values{"currency_price": {"49;250"}, "delivery": {"4"}}})
	if e != nil {
		t.Fatal(e)
	}
	u, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("text") != "USB C & size=1" || u.Query().Get("currency_price") != "49.000;250.000" || u.Query().Has("size") {
		t.Fatal(path)
	}
}
