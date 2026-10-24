package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
)

func TestShowModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args  []string
		full  bool
		calls int
	}{
		{[]string{"show", "100"}, false, 1},
		{[]string{"show", "100", "--full=false"}, false, 1},
		{[]string{"show", "100", "--full"}, true, 4},
		{[]string{"show", "--full", "100"}, true, 4},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			calls := 0
			deps := setupTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				path := r.URL.Query().Get("url")
				name := "product"
				switch {
				case path == "/product/100/":
				case strings.Contains(path, "layout_container=pdpPage2column"):
					name = "details"
				case strings.HasPrefix(path, "/modal/otherOffersFromSellers?"):
					name = "sellers"
				default:
					t.Fatalf("unexpected path %s", path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(fixture(t, name)))}, nil
			})
			var out, stderr bytes.Buffer
			if err := run(t.Context(), tc.args, &out, &stderr, deps); err != nil {
				t.Fatal(err)
			}
			var got ozon.ShowResult
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Full != tc.full || calls != tc.calls || got.HTTPRequests != tc.calls || stderr.Len() != 0 {
				t.Fatalf("mode=%v calls=%d metadata=%d stderr=%s", got.Full, calls, got.HTTPRequests, &stderr)
			}
			if tc.full && (got.Description == "" || len(got.Sellers) != 2) {
				t.Fatal("full mode did not load details and seller offers")
			}
		})
	}
}

func TestSearchIncludeFiltersFlag(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"search", "test", "--include-filters", "--limit", "1"}, {"search", "--include-filters", "test"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			calls := 0
			var out, stderr bytes.Buffer
			// This fixture lacks facets: the flag must still expose inclusion
			// state and preserve the products without fetching another page.
			if err := run(t.Context(), args, &out, &stderr, setup(t, fixture(t, "search"), &calls)); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["filters"]) != "null" || string(fields["sorting"]) != "null" || fields["warnings"] == nil || string(fields["count"]) != "1" || calls != 1 || stderr.Len() != 0 {
				t.Fatalf("combined search output=%s calls=%d stderr=%s", &out, calls, &stderr)
			}
		})
	}
}

func TestProductFlagsRejectWrongCommandsBeforeSetup(t *testing.T) {
	t.Parallel()
	noSetup := func() (dependencies, error) { t.Fatal("setup for invalid flags"); return dependencies{}, nil }
	for _, args := range [][]string{{"search", "test", "--full"}, {"filters", "test", "--include-filters"}, {"show", "100", "--include-filters"}, {"reviews", "100", "--full"}} {
		var out, stderr bytes.Buffer
		if err := run(t.Context(), args, &out, &stderr, noSetup); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
