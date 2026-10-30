package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
)

// The wire expectations come from the captured browser protocol, not the implementation.
func TestCartCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, method, path, inner, body, reply string
		args                                   []string
	}{
		{name: "list", args: []string{"cart", "list", "--user-agent", "cart-test"}, method: "GET", path: "/api/entrypoint-api.bx/page/json/v2", inner: "/cart", reply: "cart"},
		{name: "empty", args: []string{"cart", "list"}, method: "GET", path: "/api/entrypoint-api.bx/page/json/v2", inner: "/cart", reply: "cart-empty"},
		{name: "add", args: []string{"cart", "add", "https://www.ozon.ru/product/cotton-shorts-100/?tracking=ignored"}, method: "POST", path: "/api/composer-api.bx/_action/v2/addToCart", body: `[{"id":100,"quantity":1}]`},
		{name: "remove", args: []string{"cart", "remove", "100"}, method: "POST", path: "/api/entrypoint-api.bx/page/json/v2", inner: "/cart?page_changed=true", body: `{"name":"deleteItems","params":"{\"items\":[\"100\"]}"}`, reply: "cart-empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			deps := setupTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != tc.method || r.URL.Host != "www.ozon.ru" || r.URL.Path != tc.path || r.URL.Query().Get("url") != tc.inner {
					t.Fatalf("unexpected cart request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Cookie") != "session=secret" || r.Header.Get("X-O3-App-Name") != "dweb_client" {
					t.Fatal("missing session or browser app header")
				}
				if tc.name == "list" && r.Header.Get("User-Agent") != "cart-test" {
					t.Fatal("user-agent override was ignored")
				}
				if tc.method == "POST" {
					if r.Header.Get("Origin") != ozon.WebOrigin || r.Header.Get("Content-Type") != "application/json" {
						t.Fatal("missing POST headers")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					var got, want any
					if json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(tc.body), &want) != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("wrong cart payload: %s", body)
					}
				}
				body := []byte(`{"data":{"success":true,"cart":{"cartItems":[{"id":100,"qty":1}]}}}`)
				if tc.reply != "" {
					body = fixture(t, tc.reply)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			var out, stderr bytes.Buffer
			if err := run(t.Context(), tc.args, &out, &stderr, deps); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || stderr.Len() != 0 || strings.Contains(out.String(), "secret") {
				t.Fatalf("requests=%d stderr=%s", calls, &stderr)
			}
			if tc.name == "list" || tc.name == "empty" {
				var got ozon.CartResult
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if tc.name == "empty" {
					if got.Count != 0 || got.Items == nil || len(got.Items) != 0 {
						t.Fatalf("incorrect empty cart: %+v", got)
					}
					return
				}
				if got.Count != 2 || len(got.Items) != 2 {
					t.Fatalf("cart contents include recommendations or omit a split: %+v", got)
				}
				item := got.Items[0]
				if item.SKU != "100" || item.Title != "Cotton shorts" || item.Variant != "54 RU • Mix" || item.URL != "https://www.ozon.ru/product/100/" || item.Quantity == nil || *item.Quantity != 2 || item.Selected == nil || !*item.Selected || item.Prices.WithOzonCard == nil || item.Prices.WithOzonCard.Amount != 500 || item.Prices.WithoutOzonCard == nil || item.Prices.WithoutOzonCard.Amount != 600 {
					t.Fatalf("lost cart product fields: %+v", item)
				}
				if got.Items[1].SKU != "101" || got.Items[1].Quantity != nil {
					t.Fatalf("missing quantity was invented: %+v", got.Items[1])
				}
			} else {
				var got ozon.CartMutationResult
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				wantQuantity := int64(0)
				if tc.name == "add" {
					wantQuantity = 1
				}
				if got.SKU != "100" || got.Quantity != wantQuantity || got.HTTPRequests != 1 {
					t.Fatalf("incorrect mutation acknowledgement: %+v", got)
				}
			}
		})
	}
}

func TestCartFailuresDoNotReportSuccessOrReplayWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, command, body, fixture, wantError string
		status                                  int
	}{
		{name: "rejected add", command: "add", body: `{"data":{"success":false}}`, status: 200, wantError: "did not confirm adding"},
		{name: "wrong SKU acknowledgement", command: "add", body: `{"data":{"success":true,"cart":{"cartItems":[{"id":101,"qty":1}]}}}`, status: 200, wantError: "did not confirm adding"},
		{name: "zero quantity acknowledgement", command: "add", body: `{"data":{"success":true,"cart":{"cartItems":[{"id":100,"qty":0}]}}}`, status: 200, wantError: "did not confirm adding"},
		{name: "POST redirect", command: "add", status: 307, wantError: "redirect not followed"},
		{name: "delete POST redirect", command: "remove", status: 307, wantError: "redirect not followed"},
		{name: "rate limit", command: "add", status: 429, wantError: "Retry-After: 30"},
		{name: "item remains", command: "remove", fixture: "cart", status: 200, wantError: "still reports SKU 100"},
		{name: "recommendations are not empty cart proof", command: "list", body: `{"widgetStates":{"tileGridDesktop-123":{"items":[]}}}`, status: 200, wantError: "no cart items or empty-cart state"},
		{name: "malformed cart", command: "list", body: `{"widgetStates":{"cartSplit-123":{}}}`, status: 200, wantError: "no cartItems array"},
		{name: "signed-out cart", command: "remove", body: `{"widgetStates":{"emptyCart-123":{}},"userInfo":{"user":{"isLoggedIn":false}}}`, status: 200, wantError: "signed-out cart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			deps := setupTransport(t, func(*http.Request) (*http.Response, error) {
				calls++
				body := []byte(tc.body)
				if tc.fixture != "" {
					body = fixture(t, tc.fixture)
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://www.ozon.ru/cart"}, "Retry-After": {"30"}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			args := []string{"cart", tc.command}
			if tc.command != "list" {
				args = append(args, "100")
			}
			var out, stderr bytes.Buffer
			err := run(t.Context(), args, &out, &stderr, deps)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || out.Len() != 0 || calls != 1 {
				t.Fatalf("error=%v stdout=%s requests=%d", err, &out, calls)
			}
		})
	}
}
