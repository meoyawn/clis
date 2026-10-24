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

func TestAuthFollowsSessionRedirects(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusOK} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Scheme != "https" || r.URL.Host != "www.ozon.ru" || r.URL.Path != entrypointPath || r.URL.Query().Get("url") != "/my/main" {
					t.Fatal("auth redirect left the account page API")
				}
				want := map[string]string{"session": "secret-cookie-value", "__Secure-access-token": "browser-access-token", "__Secure-ETC": "browser-challenge-cookie"}
				if calls%2 == 0 {
					want["__Secure-ETC"] = "redirect-challenge-cookie"
				}
				if len(r.Cookies()) != len(want) {
					t.Fatal("redirect sent extra account or renewal credentials")
				}
				for _, cookie := range r.Cookies() {
					if value, ok := want[cookie.Name]; !ok || cookie.Value != value {
						t.Fatal("redirect replaced the imported account or leaked across invocations")
					}
				}
				if calls%2 == 1 {
					if r.URL.Query().Has("__rr") {
						t.Fatal("redirect navigation state escaped its invocation")
					}
					target := r.URL.String() + "&__rr=1"
					body, err := json.Marshal(object{"redirect": target})
					if err != nil {
						t.Fatal(err)
					}
					resp := response(body, status)
					if status != http.StatusOK {
						resp.Header.Set("Location", target)
					}
					for name, value := range map[string]string{"__Secure-ETC": "redirect-challenge-cookie", "__Secure-access-token": "replacement-access", "__Secure-user-id": "replacement-account", "__Secure-refresh-token": "replacement-refresh", "ozonIdAuthResponseToken": "replacement-exchange"} {
						resp.Header.Add("Set-Cookie", name+"="+value+"; Domain=.ozon.ru; Path=/; Secure; HttpOnly")
					}
					resp.Header.Add("Set-Cookie", "foreign=secret; Domain=evil.invalid; Path=/; Secure")
					resp.Header.Add("Set-Cookie", "unrelated=secret; Path=/my; Secure")
					return resp, nil
				}
				if r.URL.Query().Get("__rr") != "1" {
					t.Fatal("lost API redirect state")
				}
				return response(fixture(t, "auth"), http.StatusOK), nil
			})
			record, err := c.auth.load()
			if err != nil {
				t.Fatal(err)
			}
			record.Cookies = append(record.Cookies,
				storedCookie{Name: "__Secure-access-token", Value: "browser-access-token", Domain: ".ozon.ru", Path: "/", Secure: true},
				storedCookie{Name: "__Secure-ETC", Value: "browser-challenge-cookie", Domain: ".ozon.ru", Path: "/", Secure: true},
			)
			if err := c.auth.save(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(c.auth.Path)
			if err != nil {
				t.Fatal(err)
			}
			for invocation := 0; invocation < 2; invocation++ {
				s, err := c.AuthStatus(t.Context(), RequestOptions{})
				if err != nil || s.HTTPRequests != 2 || !s.Imported || !s.LoginVerified || s.Account == nil || s.Account.ID != "42" || calls != (invocation+1)*2 {
					t.Fatalf("status=%+v requests=%d error=%v", s, calls, err)
				}
				after, err := os.ReadFile(c.auth.Path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("status persisted redirect cookies", err)
				}
			}
		})
	}
}

func TestAuthRejectsUnrelatedRedirects(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"https://evil.invalid/my/main?secret=private", "//evil.invalid/my/main", "http://www.ozon.ru/my/main",
		"/my/auth", "/my/orders", "/search/?text=private", "/my/../my/main", "/my/main/extra",
		"/my/main?token=private", "/my/main?__rr=1&__rr=2", "/my/main?__rr=", "/my/main?__rr=%zz",
		entrypointPath + "?url=%2Fmy%2Fmain&url=%2Fmy%2Forders",
		entrypointPath + "?url=%2Fmy%2Fmain&token=private",
		entrypointPath + "?url=https%3A%2F%2Fevil.invalid%2Fmy%2Fmain",
	} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			calls := 0
			c := testClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				resp := response(nil, http.StatusTemporaryRedirect)
				resp.Header.Set("Location", target)
				return resp, nil
			})
			before, err := os.ReadFile(c.auth.Path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := c.AuthStatus(t.Context(), RequestOptions{})
			if err == nil || calls != 1 || s.HTTPRequests != 1 || !s.Imported || s.LoginVerified {
				t.Fatalf("status=%+v requests=%d error=%v", s, calls, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("redirect diagnostics leaked query state")
			}
			after, err := os.ReadFile(c.auth.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("status changed saved session")
			}
		})
	}
}

func TestAuthRedirectFailuresAndBounds(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"success", "loop", "limit", "signed-out", "unknown", "http", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			c := testClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 || mode == "loop" || mode == "limit" || mode == "success" && calls <= 3 {
					next := entrypointURL("/my/main/")
					q := next.Query()
					q.Set("__rr", fmt.Sprint(calls))
					next.RawQuery = q.Encode()
					if mode == "loop" {
						next = entrypointURL("/my/main")
					}
					resp := response(nil, http.StatusFound)
					resp.Header.Set("Location", next.String())
					if mode == "canceled" {
						cancel()
					}
					return resp, nil
				}
				switch mode {
				case "http":
					resp := response(nil, http.StatusTooManyRequests)
					resp.Header.Set("Retry-After", "60")
					return resp, nil
				case "unknown":
					return response([]byte(`{"widgetStates":{"profileLogo":"{}"}}`), http.StatusOK), nil
				case "signed-out":
					return response(bytes.ReplaceAll(fixture(t, "auth"), []byte(`true`), []byte(`false`)), http.StatusOK), nil
				}
				return response(fixture(t, "auth"), http.StatusOK), nil
			})
			before, err := os.ReadFile(c.auth.Path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := c.AuthStatus(ctx, RequestOptions{})
			wantCalls := 2
			switch mode {
			case "loop", "canceled":
				wantCalls = 1
			case "limit", "success":
				wantCalls = 4
			}
			if calls != wantCalls || s.HTTPRequests != wantCalls || (err == nil) != (mode == "success") {
				t.Fatalf("status=%+v requests=%d error=%v", s, calls, err)
			}
			if mode == "http" && s.RetryAfter != "60" || mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost failure details: %+v %v", s, err)
			}
			if mode != "success" {
				after, err := os.ReadFile(c.auth.Path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatal("status changed saved session")
				}
			}
		})
	}
}

func TestAuthPageRedirect(t *testing.T) {
	t.Parallel()
	calls := 0
	c := testClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response([]byte(`{"redirect":"/my/main/?page_changed=true"}`), http.StatusOK), nil
		}
		u, err := url.Parse(r.URL.Query().Get("url"))
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != "/my/main/" || u.Query().Get("page_changed") != "true" {
			t.Fatal("lost account page navigation state")
		}
		return response(fixture(t, "auth"), http.StatusOK), nil
	})
	s, err := c.AuthStatus(t.Context(), RequestOptions{})
	if err != nil || calls != 2 || s.HTTPRequests != 2 {
		t.Fatalf("status=%+v requests=%d error=%v", s, calls, err)
	}
}
