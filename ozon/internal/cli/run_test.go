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
	"time"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
	"github.com/steipete/sweetcookie"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func setup(t *testing.T, body []byte, calls *int) setupFunc {
	t.Helper()
	return setupTransport(t, func(*http.Request) (*http.Response, error) {
		*calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
}

func setupTransport(t *testing.T, transport roundTripFunc) setupFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	store := map[string]any{"version": 1, "browser": "chrome", "imported_at": time.Now(), "cookies": []map[string]any{{"name": "session", "value": "secret", "domain": ".ozon.ru", "path": "/", "secure": true}}}
	b, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	h := &http.Client{Transport: transport}
	c, e := ozon.NewClient(h, ozon.AuthStore{Path: path})
	if e != nil {
		t.Fatal(e)
	}
	return func() (dependencies, error) {
		return dependencies{client: c, readCookies: func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
			return sweetcookie.Result{Cookies: []sweetcookie.Cookie{{Name: "session", Value: "secret", Domain: ".ozon.ru", Path: "/", Secure: true}}}, nil
		}}, nil
	}
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	// #nosec G304 -- Fixture names are supplied only by these tests.
	b, e := os.ReadFile(filepath.Join("..", "service", "testdata", name+".json"))
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestAllCommandOutputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args    []string
		fixture string
		key     string
	}{{[]string{"search", "test", "--limit", "1"}, "search", "results"}, {[]string{"filters", "--filter", "delivery=4", "test"}, "filters", "filters"}, {[]string{"reviews", "100", "--limit=1"}, "reviews", "reviews"}, {[]string{"auth", "status", "--json"}, "auth", "account"}} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			calls := 0
			var out, stderr bytes.Buffer
			if e := run(t.Context(), tc.args, &out, &stderr, setup(t, fixture(t, tc.fixture), &calls)); e != nil {
				t.Fatal(e)
			}
			var result map[string]any
			if json.Unmarshal(out.Bytes(), &result) != nil || result[tc.key] == nil || calls != 1 {
				t.Fatalf("stdout %s stderr %s calls %d", &out, &stderr, calls)
			}
			if stderr.Len() != 0 || strings.Contains(out.String(), "secret") {
				t.Fatal("unexpected diagnostics")
			}
		})
	}
}

func TestFlagsAndHelpFailBeforeSetup(t *testing.T) {
	t.Parallel()
	noSetup := func() (dependencies, error) { t.Fatal("setup for help or invalid input"); return dependencies{}, nil }
	for _, args := range [][]string{{"--help"}, {"search", "--help"}, {"auth", "--help"}, {"auth", "import", "--help"}, {"cart", "--help"}, {"cart", "add", "--help"}} {
		var out, errOut bytes.Buffer
		if e := run(t.Context(), args, &out, &errOut, noSetup); e != nil || out.Len()+errOut.Len() == 0 {
			t.Fatalf("%v: %v", args, e)
		}
	}
	for _, args := range [][]string{{"cart"}, {"cart", "unknown"}, {"cart", "add"}, {"cart", "add", "0"}, {"cart", "remove", "100", "101"}, {"cart", "list", "100"}, {"cart", "add", "https://example.com/product/100/"}, {"cart", "list", "--timeout=0s"}, {"cart", "add", "100", "--user-agent=bad\nagent"}} {
		var out, errOut bytes.Buffer
		if err := run(t.Context(), args, &out, &errOut, noSetup); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{{"search"}, {"search", "x", "--page", "0"}, {"search", "x", "--limit", "-1"}, {"search", "x", "--cursor", "/search/?text=x&page=2", "--page", "1"}, {"filters", "x", "--filter", "oops"}, {"show", "0"}, {"show", "100", "101"}, {"reviews", "100", "--scope", "unknown"}, {"auth", "import"}, {"auth", "import", "--browser", "unknown"}, {"auth", "status", "extra"}, {"search", "x", "--timeout", "0s"}, {"reviews", "100", "--cursor", "/product/100/reviews/?page=2", "--sort", "score_desc"}} {
		var out, errOut bytes.Buffer
		if e := run(t.Context(), args, &out, &errOut, noSetup); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestReadableAuthStatus(t *testing.T) {
	t.Parallel()
	calls := 0
	var out, stderr bytes.Buffer
	deps, err := setup(t, fixture(t, "auth"), &calls)()
	if err != nil {
		t.Fatal(err)
	}
	deps.readCookies = func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		t.Fatal("status read browser cookies")
		return sweetcookie.Result{}, nil
	}
	if e := run(t.Context(), []string{"auth", "status"}, &out, &stderr, func() (dependencies, error) { return deps, nil }); e != nil {
		t.Fatal(e)
	}
	if out.String() != "Signed in as Example User.\n" || stderr.Len() != 0 || calls != 1 {
		t.Fatal(out.String(), stderr.String(), calls)
	}
}

func TestHelpPropagatesWriteErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"search", "--help"}, {"auth", "import", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			noSetup := func() (dependencies, error) {
				t.Fatal("setup for help")
				return dependencies{}, nil
			}
			if err := run(t.Context(), args, io.Discard, failedWriter{}, noSetup); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("expected help write error, got %v", err)
			}
		})
	}
}
