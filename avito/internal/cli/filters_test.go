package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	avito "github.com/meoyawn/clis/avito/internal/service"
)

func TestFiltersToSearchAndValueLookupRequestBounds(t *testing.T) {
	t.Parallel()
	calls, lookups := 0, 0
	setup := commandSetup(t, func(req *http.Request) (*http.Response, error) {
		calls++
		name := "web_filters.json"
		switch req.URL.Path {
		case "/web/1/slocations":
			lookups++
			name = "web_locations.json"
		case "/web/1/js/items":
			if calls > 2 {
				q := req.URL.Query()
				for key, want := range map[string]string{"categoryId": "21", "params[48]": "5067", "params[486]": "5072", "params[172087]": "3281454", "params[145393]": "18204028", "pmax": "5000", "user": "1", "s": "1"} {
					if q.Get(key) != want {
						t.Fatalf("search_args lost %s=%s: %s", key, want, req.URL)
					}
				}
			}
		case "/web/2/suggest/desktop":
			name = "web_filter_values.json"
			q := req.URL.Query()
			if q.Get("paramId") != "145393" || q.Get("query") != "bek" || q.Get("params[172087]") != "3281454" || q.Get("offset") != "0" {
				t.Fatalf("option lookup lost its scope: %s", req.URL)
			}
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		body, err := os.ReadFile("../service/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	})
	store := testConfigStore(t)
	config := avito.DefaultConfig()
	config.City = "Казань"
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"filters", "холодильник"}, &out, io.Discard, setup, store); err != nil {
		t.Fatal(err)
	}
	var discovered avito.FiltersResult
	if err := json.Unmarshal(out.Bytes(), &discovered); err != nil || calls != 2 || lookups != 1 || discovered.HTTPRequests != 2 {
		t.Fatalf("filter discovery request count: calls=%d lookups=%d output=%s err=%v", calls, lookups, out.String(), err)
	}
	for i, command := range []string{"search", "filters", "values"} {
		out.Reset()
		args := append([]string{command, "холодильник"}, discovered.SearchArgs...)
		switch command {
		case "values":
			args[0] = "filters"
			args = append(args, "--field", "params[145393]", "--value-query", "bek")
		case "filters":
			args = append(args, "--only", "params[149569]")
		}
		if err := run(t.Context(), args, &out, io.Discard, setup, store); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		var meta avito.Metadata
		if err := json.Unmarshal(out.Bytes(), &meta); err != nil || meta.HTTPRequests != 1 || calls != i+3 || lookups != 1 || strings.Contains(out.String(), "SYNTHETIC_") {
			t.Fatalf("%s exceeded its request bound or leaked state: calls=%d output=%s err=%v", command, calls, out.String(), err)
		}
		if command == "filters" {
			var filtered avito.FiltersResult
			if err := json.Unmarshal(out.Bytes(), &filtered); err != nil || len(filtered.Filters) != 1 || filtered.Filters[0].Key != "params[149569]" {
				t.Fatal("--only did not select the keyword field")
			}
		}
	}
}

func TestFilterArgumentFailuresAreLocal(t *testing.T) {
	t.Parallel()
	setup := func() (dependencies, error) {
		t.Fatal("invalid filters initialized I/O")
		return dependencies{}, nil
	}
	store := testConfigStore(t)
	for _, command := range []string{"search", "filters"} {
		for _, flagArgs := range [][]string{
			{"--filter", "brand"}, {"--filter", "brand="}, {"--filter", "categoryId=21"},
			{"--filter", "pmax=-1"}, {"--filter", "params[1]=x", "--filter", "params[1]=x"},
			{"--filter", "pmin=5000", "--filter", "pmax=1000"}, {"--sort", "invalid"},
		} {
			args := append([]string{command, "холодильник", "--city", "Казань"}, flagArgs...)
			if err := run(t.Context(), args, io.Discard, io.Discard, setup, store); err == nil {
				t.Fatalf("accepted invalid arguments: %v", args)
			}
		}
	}
	for _, args := range [][]string{
		{"filters", "--field", "params[1]", "холодильник"},
		{"filters", "--category-id", "21", "--field", "params[1][from]"},
		{"filters", "--category-id", "21", "--field", "params[1]", "--offset", "-1"},
		{"filters", "--category-id", "21", "--field", "params[1]", "--only", "params[1]"},
		{"filters", "холодильник", "--value-query", "Beko"},
		{"filters", "холодильник", "--offset", "1"},
		{"filters", "холодильник", "--only", ""},
	} {
		if err := run(t.Context(), args, io.Discard, io.Discard, setup, store); err == nil {
			t.Fatalf("accepted invalid value lookup: %v", args)
		}
	}
	if err := run(t.Context(), []string{"filters", "--help"}, io.Discard, io.Discard, setup, store); err != nil {
		t.Fatal(err)
	}
}
