package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestSearchAPIRedirectState(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"search", "filters"} {
		for _, status := range []int{http.StatusTemporaryRedirect, http.StatusOK} {
			t.Run(fmt.Sprintf("%s/%d", command, status), func(t *testing.T) {
				t.Parallel()
				calls := 0
				var path string
				c := testClient(t, func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "www.ozon.ru" || r.URL.Path != entrypointPath {
						t.Fatal("redirect left the Ozon page API")
					}
					wantCookies := map[string]string{"session": "secret-cookie-value", "__Secure-ETC": "old-cookie-value"}
					if calls%2 == 0 {
						wantCookies["__Secure-ETC"] = "new-cookie-value"
					}
					if len(r.Cookies()) != len(wantCookies) {
						t.Fatal("unexpected, duplicate or missing redirect cookies")
					}
					for _, cookie := range r.Cookies() {
						if value, ok := wantCookies[cookie.Name]; !ok || cookie.Value != value {
							t.Fatal("redirect cookie was not updated or escaped its invocation")
						}
						delete(wantCookies, cookie.Name)
					}
					if calls%2 == 1 {
						if r.URL.Query().Has("__rr") {
							t.Fatal("API redirect state escaped its invocation")
						}
						path = r.URL.Query().Get("url")
						target := r.URL.String() + "&__rr=1"
						body, err := json.Marshal(object{"redirect": target})
						if err != nil {
							t.Fatal(err)
						}
						resp := response(body, status)
						if status != http.StatusOK {
							resp.Header.Set("Location", target)
							resp.Body = http.NoBody
						}
						resp.Header.Add("Set-Cookie", "__Secure-ETC=new-cookie-value; Domain=.ozon.ru; Path=/; Secure; HttpOnly")
						resp.Header.Add("Set-Cookie", "foreign=secret; Domain=evil.invalid; Path=/; Secure")
						resp.Header.Add("Set-Cookie", "unrelated=secret; Path=/my; Secure")
						return resp, nil
					}
					if r.URL.Query().Get("__rr") != "1" || r.URL.Query().Get("url") != path {
						t.Fatal("API redirect state was lost or moved into the nested page URL")
					}
					p := fixturePage(t, command)
					// Ozon echoes API navigation state into the returned page and cursor.
					p.PageInfo.URL = path + "&__rr=1"
					p.NextPage = p.PageInfo.URL + "&page=2"
					body, err := json.Marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					return response(body, http.StatusOK), nil
				})
				record, err := c.auth.load()
				if err != nil {
					t.Fatal(err)
				}
				record.Cookies = append(record.Cookies, storedCookie{Name: "__Secure-ETC", Value: "old-cookie-value", Domain: ".ozon.ru", Path: "/", Secure: true})
				if err := c.auth.save(t.Context(), record); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(c.auth.Path)
				if err != nil {
					t.Fatal(err)
				}
				for invocation := 0; invocation < 2; invocation++ {
					o := SearchOptions{Query: "кора осины", Page: 1}
					var meta Metadata
					if command == "search" {
						result, err := c.Search(t.Context(), o, RequestOptions{})
						if err != nil {
							t.Fatal(err)
						}
						next, err := url.Parse(path)
						if err != nil {
							t.Fatal(err)
						}
						q := next.Query()
						q.Set("page", "2")
						next.RawQuery = q.Encode()
						if result.Query != o.Query || result.Count != 1 || result.NextCursor != next.String() {
							t.Fatal("search lost the requested query, results or continuation")
						}
						meta = result.Metadata
					} else {
						o.Filters = url.Values{"delivery": {"4"}}
						result, err := c.Filters(t.Context(), o, RequestOptions{})
						if err != nil {
							t.Fatal(err)
						}
						if result.Query != o.Query || len(result.Filters) == 0 {
							t.Fatal("filters lost the requested query or facets")
						}
						meta = result.Metadata
					}
					if calls != (invocation+1)*2 || meta.HTTPRequests != 2 || len(meta.SourceURLs) != 2 || meta.SourceURLs[0] != WebOrigin+path || meta.SourceURLs[1] != WebOrigin+path {
						t.Fatal("API redirect was not counted with its source page")
					}
				}
				after, err := os.ReadFile(c.auth.Path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("search redirect changed the saved browser session")
				}
			})
		}
	}
}

func TestSearchCursorDropsAPIRedirectState(t *testing.T) {
	t.Parallel()
	const cursor = "/search/?layout_page_index=2&page=2&paginator_token=opaque&search_page_state=state%2B%3D&text=test"
	const next = "/search/?layout_page_index=3&page=3&paginator_token=next&search_page_state=state%2B%3D&text=test"
	for _, state := range []string{"&__rr=1", "&__rr=1&__rr=1"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if path := requireRequest(t, r); path != cursor {
					t.Fatal("cursor retained request-local API state or lost continuation state")
				}
				if calls == 1 {
					if r.URL.Query().Has("__rr") {
						t.Fatal("cursor carried API state into the next invocation")
					}
					resp := response(nil, http.StatusTemporaryRedirect)
					resp.Header.Set("Location", r.URL.String()+"&__rr=1")
					return resp, nil
				}
				if calls != 2 || r.URL.Query().Get("__rr") != "1" {
					t.Fatal("pagination lost fresh API redirect state")
				}
				p := fixturePage(t, "search")
				p.PageInfo.URL = cursor + state
				p.NextPage = next + state
				grid, err := p.first("tileGridDesktop")
				if err != nil {
					t.Fatal(err)
				}
				grid["page"] = 2
				putWidget(t, p, "tileGridDesktop-123-default-1", grid)
				body, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(body, http.StatusOK), nil
			})
			result, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, Cursor: cursor + state}, RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || result.HTTPRequests != 2 || result.Page != 2 || result.Count != 1 || result.NextCursor != next {
				t.Fatal("API redirect did not preserve pagination")
			}
		})
	}
}

func TestSearchRedirectFormats(t *testing.T) {
	t.Parallel()
	const target = "/search/?text=test&from_global=true&page_changed=true"
	api := entrypointPath + "?" + url.Values{"url": {target}}.Encode()
	for _, tc := range []struct {
		name     string
		status   int
		location string
	}{
		{"permanent page", 301, target},
		{"relative API", 302, api},
		{"absolute API", 303, WebOrigin + api},
		{"absolute page", 307, WebOrigin + target},
		{"apex host", 308, "https://ozon.ru" + target},
		{"JSON page", 200, target},
		{"JSON API", 200, api},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				path := requireRequest(t, r)
				if calls == 1 {
					body, err := json.Marshal(object{"redirect": tc.location})
					if err != nil {
						t.Fatal(err)
					}
					resp := response(body, tc.status)
					resp.Header.Set("Location", tc.location)
					return resp, nil
				}
				if calls != 2 || path != target {
					t.Fatalf("unexpected request %d: %s", calls, path)
				}
				p := fixturePage(t, "search")
				p.PageInfo.URL = target
				loggedIn := false
				p.UserInfo.User.LoggedIn = &loggedIn
				body, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(body, http.StatusOK), nil
			})
			result, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1}, RequestOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || result.HTTPRequests != 2 || result.Count != 1 || result.IsAuthenticated == nil || *result.IsAuthenticated || len(result.Warnings) != 1 {
				t.Fatalf("unexpected result after redirect: calls=%d result=%+v", calls, result)
			}
		})
	}
}

func TestSearchRejectsInvalidRedirectsBeforeFollowing(t *testing.T) {
	t.Parallel()
	api := entrypointPath + "?" + url.Values{"url": {"/search/?text=test"}}.Encode()
	for _, tc := range []struct {
		name    string
		target  string
		filters url.Values
	}{
		{"external page", "https://evil.invalid/search/?text=test&token=secret-target", nil},
		{"external API", "https://evil.invalid" + entrypointPath + "?url=%2Fsearch%2F%3Ftext%3Dtest", nil},
		{"external inner page", entrypointPath + "?" + url.Values{"url": {"https://evil.invalid/search/?text=test"}}.Encode(), nil},
		{"HTTP origin", "http://www.ozon.ru/search/?text=test", nil},
		{"network path", "//evil.invalid/search/?text=test", nil},
		{"other Ozon endpoint", "/my/main?text=test", nil},
		{"product", "/product/100/?text=test", nil},
		{"category traversal", "/category/../my/main/?text=test", nil},
		{"missing query", "/category/sample-100/", nil},
		{"changed query", "/category/sample-100/?text=shoes", nil},
		{"duplicate query", "/search/?text=test&text=shoes", nil},
		{"duplicate page URL", entrypointPath + "?url=%2Fsearch%2F%3Ftext%3Dtest&url=secret-target", nil},
		{"missing page URL", entrypointPath, nil},
		{"API query override", api + "&text=shoes", nil},
		{"API filter override", api + "&delivery=4", nil},
		{"API page override", api + "&page=2", nil},
		{"duplicate API navigation", api + "&__rr=1&__rr=2", nil},
		{"empty API navigation", api + "&__rr=", nil},
		{"changed page", "/search/?text=test&page=2", nil},
		{"recommendation", "/search/?text=test&non_found=1", nil},
		{"sold out feed", "/search/?text=test&sold_out_page=1", nil},
		{"recommendation tiles", "/search/?text=test&search_tiles_req=opaque", nil},
		{"added filter", "/category/sample-100/?text=test&delivery=4", nil},
		{"removed filter", "/category/sample-100/?text=test", url.Values{"delivery": {"4"}}},
		{"changed filter", "/category/sample-100/?text=test&delivery=7", url.Values{"delivery": {"4"}}},
		{"changed sorting", "/category/sample-100/?text=test&sorting=score", url.Values{"sorting": {"price"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				requireRequest(t, r)
				resp := response([]byte("secret-response-body"), http.StatusFound)
				resp.Header.Set("Location", tc.target)
				return resp, nil
			})
			result, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1, Filters: tc.filters}, RequestOptions{})
			var failure *HTTPError
			if result != nil || !errors.As(err, &failure) || failure.Status != 302 || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("redirect leaked private data: %v", err)
			}
		})
	}
}

func TestSearchRedirectBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		redirects int
		loop      bool
		api       bool
		wantCalls int
		wantError string
	}{
		{"last allowed redirect", 3, false, false, 4, ""},
		{"too many redirects", 4, false, false, 4, "exceeded"},
		{"loop", 2, true, false, 2, "loop"},
		{"last allowed API redirect", 3, false, true, 4, ""},
		{"too many API redirects", 4, false, true, 4, "exceeded"},
		{"API loop", 2, true, true, 2, "loop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				path := requireRequest(t, r)
				if calls <= tc.redirects {
					target := fmt.Sprintf("/category/sample-%d/?text=test&from_global=true", calls)
					if tc.loop && calls == 2 {
						// Same initial URL with a different parameter order.
						target = "/search/?text=test&from_global=true"
					}
					if tc.api {
						target = entrypointPath + "?" + url.Values{"url": {path}, "__rr": {fmt.Sprint(calls)}}.Encode()
						if tc.loop && calls == 2 {
							// Reorder both API and page parameters without advancing.
							target = entrypointPath + "?url=" + url.QueryEscape("/search/?text=test&from_global=true") + "&__rr=1"
						}
					}
					resp := response(nil, http.StatusFound)
					resp.Header.Set("Location", target)
					return resp, nil
				}
				p := fixturePage(t, "search")
				p.PageInfo.URL, p.PageInfo.PageType = path, "category"
				if tc.api {
					p.PageInfo.PageType = "search"
				}
				p.NextPage = path + "&page=2"
				body, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				return response(body, http.StatusOK), nil
			})
			result, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1}, RequestOptions{})
			if calls != tc.wantCalls {
				t.Fatalf("requests=%d want=%d", calls, tc.wantCalls)
			}
			if tc.wantError == "" {
				if err != nil || result.HTTPRequests != calls || len(result.SourceURLs) != calls {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || result != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSinglePageFetchRejectsRedirects(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/my/main", "/product/100/", "/product/100/reviews/"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				requireRequest(t, r)
				resp := response(nil, http.StatusFound)
				resp.Header.Set("Location", WebOrigin+path+"?page_changed=true")
				return resp, nil
			})
			_, err := c.fetchPage(t.Context(), path, entrypointURL(path), RequestOptions{}, c.auth.cookiesFor)
			if calls != 1 || err == nil {
				t.Fatalf("requests=%d err=%v", calls, err)
			}
		})
	}
}

func TestSearchStopsOnFailureAfterRedirect(t *testing.T) {
	t.Parallel()
	calls := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		requireRequest(t, r)
		status := http.StatusFound
		if calls > 1 {
			status = http.StatusTooManyRequests
		}
		resp := response([]byte("secret-response-body"), status)
		resp.Header.Set("Location", "/category/sample-100/?text=test")
		resp.Header.Set("Retry-After", "60")
		return resp, nil
	})
	result, err := c.Search(t.Context(), SearchOptions{Query: "test", Page: 1}, RequestOptions{})
	var failure *HTTPError
	if calls != 2 || result != nil || !errors.As(err, &failure) || failure.Status != 429 || failure.RetryAfter != "60" {
		t.Fatalf("requests=%d err=%v", calls, err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("response leaked private data: %v", err)
	}
}

func TestSearchCancellationBetweenRedirects(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		requireRequest(t, r)
		resp := response(nil, http.StatusFound)
		resp.Header.Set("Location", "/category/sample-100/?text=test")
		cancel()
		return resp, nil
	})
	result, err := c.Search(ctx, SearchOptions{Query: "test", Page: 1}, RequestOptions{})
	if calls != 1 || result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("requests=%d err=%v", calls, err)
	}
}
