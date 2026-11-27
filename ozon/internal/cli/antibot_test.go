package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
)

// antibotServer answers like Ozon: every request whose abt_data is not
// accepted gets the anti-bot challenge; accepted ones get a fixture.
func antibotServer(t *testing.T, accepted, page string, rejected *int) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		reply := func(status int, body []byte) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
		}
		if accepted == "" || !strings.Contains(r.Header.Get("Cookie"), "abt_data="+accepted) {
			*rejected++
			return reply(http.StatusForbidden, []byte(`{"challengeURL":"https://www.ozon.ru/challenge.html"}`))
		}
		if !strings.Contains(r.Header.Get("Cookie"), "__Secure-access-token=account") {
			t.Fatal("account cookie was replaced or dropped")
		}
		inner := r.URL.Query().Get("url")
		switch {
		case strings.HasSuffix(r.URL.Path, "/addToCart"):
			return reply(http.StatusOK, []byte(`{"data":{"success":true,"cart":{"cartItems":[{"id":100,"qty":1}]}}}`))
		case strings.HasSuffix(r.URL.Path, "/pdpGetButtonTexts"):
			return reply(http.StatusOK, []byte(`{"data":{"firstButton":{"toCartText":{"text":"Завтра"}}}}`))
		case strings.HasPrefix(inner, "/cart"):
			return reply(http.StatusOK, fixture(t, "cart-empty"))
		case strings.HasPrefix(inner, "/my/main"):
			return reply(http.StatusOK, fixture(t, "auth"))
		case strings.Contains(inner, "/reviews/"):
			return reply(http.StatusOK, fixture(t, "reviews"))
		case strings.Contains(inner, "layout_container=pdpPage2column"):
			return reply(http.StatusOK, fixture(t, "details"))
		case strings.HasPrefix(inner, "/modal/otherOffersFromSellers"):
			return reply(http.StatusOK, fixture(t, "sellers"))
		case strings.HasPrefix(inner, "/product/"):
			return reply(http.StatusOK, fixture(t, "product"))
		}
		return reply(http.StatusOK, fixture(t, page))
	}
}

func antibotDeps(t *testing.T, transport roundTripFunc, engine func() ([]*http.Cookie, error)) (string, setupFunc, *int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	saved, err := json.Marshal(map[string]any{"version": 1, "browser": "chrome", "imported_at": time.Now(), "cookies": []map[string]any{
		{"name": "abt_data", "value": "stale", "domain": ".ozon.ru", "path": "/", "secure": true},
		{"name": "__Secure-access-token", "value": "account", "domain": ".ozon.ru", "path": "/", "secure": true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, saved, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := ozon.NewClient(&http.Client{Transport: transport}, ozon.AuthStore{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	renewals := 0
	client.SetAntibotSource(func(context.Context) ([]*http.Cookie, error) {
		renewals++
		return engine()
	})
	return path, func() (dependencies, error) { return dependencies{client: client}, nil }, &renewals
}

func freshEngine() ([]*http.Cookie, error) {
	return []*http.Cookie{
		{Name: "abt_data", Value: "fresh", Domain: ".ozon.ru", Path: "/", Secure: true},
		// The engine's own anonymous session must not replace the account.
		{Name: "__Secure-access-token", Value: "anonymous", Domain: ".ozon.ru", Path: "/", Secure: true},
	}, nil
}

// Every command reaches Ozon through the client's single request path, so a
// stale abt_data is renewed there for all of them: exactly once per command,
// with the rejected request repeated and later requests sent fresh.
func TestEveryCommandRenewsStaleAntibotCookie(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		page string
	}{
		{[]string{"search", "test"}, "search"},
		{[]string{"filters", "test", "--filter", "delivery=4"}, "filters"},
		{[]string{"show", "100"}, ""},
		{[]string{"show", "100", "--full"}, ""},
		{[]string{"reviews", "100"}, ""},
		{[]string{"auth", "status", "--json"}, ""},
		{[]string{"cart", "list"}, ""},
		{[]string{"cart", "add", "100"}, ""},
		{[]string{"cart", "remove", "100"}, ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			rejected := 0
			path, deps, renewals := antibotDeps(t, antibotServer(t, "fresh", tc.page, &rejected), freshEngine)
			var out, stderr bytes.Buffer
			if err := run(t.Context(), tc.args, &out, &stderr, deps); err != nil {
				t.Fatalf("err=%v stderr=%s", err, &stderr)
			}
			stored, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if *renewals != 1 || rejected != 1 || !bytes.Contains(stored, []byte(`"fresh"`)) || !bytes.Contains(stored, []byte(`"account"`)) {
				t.Fatalf("renewals=%d rejected=%d stored=%s", *renewals, rejected, stored)
			}
		})
	}
}

func TestAntibotRenewalFailsWithoutLooping(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		accepted string
		engine   func() ([]*http.Cookie, error)
		rejected int
	}{
		{name: "engine fails", engine: func() ([]*http.Cookie, error) { return nil, errors.New("no web view") }, rejected: 1},
		{name: "renewed cookie rejected", accepted: "other", engine: freshEngine, rejected: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejected := 0
			_, deps, renewals := antibotDeps(t, antibotServer(t, tc.accepted, "search", &rejected), tc.engine)
			var out bytes.Buffer
			err := run(t.Context(), []string{"search", "test"}, &out, io.Discard, deps)
			if !ozon.IsBrowserChallenge(err) || *renewals != 1 || rejected != tc.rejected || out.Len() != 0 {
				t.Fatalf("err=%v renewals=%d rejected=%d output=%q", err, *renewals, rejected, out.String())
			}
		})
	}
}

// Recorded live order: a stale abt_data is redirected to __rr=1, which is
// challenged; after renewal Ozon redirects again and rotates abt_data. The
// rotated value must reach the next hop instead of the renewed one.
func TestAntibotRenewalKeepsCookieOzonRotatesMidRedirect(t *testing.T) {
	t.Parallel()
	rejected := 0
	transport := func(r *http.Request) (*http.Response, error) {
		reply := func(status int, header http.Header, body []byte) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body))}, nil
		}
		abt, err := r.Cookie("abt_data")
		if err != nil {
			t.Fatal("request without abt_data")
		}
		page := r.URL.Query().Get("url")
		switch abt.Value {
		case "stale":
			if r.URL.Query().Has("__rr") {
				rejected++
				return reply(http.StatusForbidden, http.Header{}, []byte(`{"challengeURL":"https://www.ozon.ru/challenge.html"}`))
			}
			return reply(http.StatusTemporaryRedirect, http.Header{"Location": {"/api/entrypoint-api.bx/page/json/v2?" + url.Values{"url": {page}, "__rr": {"1"}}.Encode()}}, nil)
		case "fresh":
			next := "/search/?deny_category_prediction=true&from_global=true&text=test"
			return reply(http.StatusFound, http.Header{
				"Location":   {"/api/entrypoint-api.bx/page/json/v2?" + url.Values{"url": {next}, "__rr": {"1"}}.Encode()},
				"Set-Cookie": {"abt_data=rotated; Domain=.ozon.ru; Path=/; Secure"},
			}, nil)
		case "rotated":
			return reply(http.StatusOK, http.Header{}, fixture(t, "search"))
		}
		t.Fatalf("Ozon's rotated abt_data was replaced by %q", abt.Value)
		return nil, nil
	}
	_, deps, renewals := antibotDeps(t, transport, freshEngine)
	var out, stderr bytes.Buffer
	if err := run(t.Context(), []string{"search", "test"}, &out, &stderr, deps); err != nil {
		t.Fatalf("err=%v stderr=%s", err, &stderr)
	}
	if *renewals != 1 || rejected != 1 {
		t.Fatalf("renewals=%d rejected=%d", *renewals, rejected)
	}
}
