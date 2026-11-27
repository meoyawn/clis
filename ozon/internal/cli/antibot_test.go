package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
	"github.com/steipete/sweetcookie"
)

// Ozon rejects a stale abt_data with a 403 challenge that only a real browser
// can pass. The CLI must let the imported browser renew it, wait for the
// browser to persist the new value, save it, and retry the read once.
func TestChallengeRenewsAntibotCookieThroughBrowser(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		renews  bool
		timeout string
	}{
		{name: "browser renews", renews: true, timeout: "10s"},
		{name: "browser never renews", timeout: "200ms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "auth.json")
			saved, err := json.Marshal(map[string]any{"version": 1, "browser": "chrome", "imported_at": time.Now(),
				"cookies": []map[string]any{{"name": "abt_data", "value": "stale", "domain": ".ozon.ru", "path": "/", "secure": true}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, saved, 0600); err != nil {
				t.Fatal(err)
			}
			requests, opened, reads := 0, 0, 0
			client, err := ozon.NewClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if strings.Contains(r.Header.Get("Cookie"), "abt_data=fresh") {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(fixture(t, "search")))}, nil
				}
				return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"challengeURL":"https://www.ozon.ru/challenge.html"}`))}, nil
			})}, ozon.AuthStore{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			deps := dependencies{client: client, renewPoll: time.Millisecond,
				openBrowser: func(_ context.Context, b ozon.Browser, target string) error {
					if b.String() != "chrome" || target != "https://www.ozon.ru/" {
						t.Fatalf("opened %s in %s", target, b)
					}
					opened++
					return nil
				},
				// The browser persists its renewed cookie a few reads after the page loads.
				readCookies: func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
					reads++
					value := "stale"
					if tc.renews && opened > 0 && reads > 3 {
						value = "fresh"
					}
					return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "abt_data", Value: value, Domain: ".ozon.ru", Path: "/", Secure: true}}}, nil
				}}
			var out bytes.Buffer
			err = run(t.Context(), []string{"search", "test", "--timeout", tc.timeout}, &out, io.Discard, func() (dependencies, error) { return deps, nil })
			if opened != 1 {
				t.Fatalf("browser opened %d times", opened)
			}
			stored, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tc.renews {
				if err == nil || !strings.Contains(err.Error(), "did not renew") || requests != 1 || out.Len() != 0 || !bytes.Contains(stored, []byte(`"stale"`)) {
					t.Fatalf("err=%v requests=%d output=%q", err, requests, out.String())
				}
				return
			}
			if err != nil || requests != 2 || !strings.Contains(out.String(), `"results"`) || !bytes.Contains(stored, []byte(`"fresh"`)) {
				t.Fatalf("err=%v requests=%d output=%q", err, requests, out.String())
			}
		})
	}
}
