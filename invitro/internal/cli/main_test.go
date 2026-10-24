package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempPaths(t *testing.T) appPaths {
	t.Helper()
	dir := t.TempDir()
	return appPaths{config: filepath.Join(dir, "config.json"), index: filepath.Join(dir, "products")}
}

func execute(t *testing.T, paths appPaths, origin string, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runWith(context.Background(), args, &stdout, &stderr, func() (appPaths, error) { return paths, nil }, origin)
	return stdout.String(), err
}

func TestConfigAndHelpAreLocal(t *testing.T) {
	p := tempPaths(t)
	for _, args := range [][]string{{"help"}, {"search", "--help"}, {"show", "--help"}, {"config"}, {"config", "set", "city", " KAZAN "}} {
		if _, err := execute(t, p, "not-an-origin", args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	output, err := execute(t, p, "unused", "config")
	if err != nil || !strings.Contains(output, `"city": "kazan"`) {
		t.Fatalf("config %s: %v", output, err)
	}
	before, _ := os.ReadFile(p.config)
	for _, args := range [][]string{{"config", "set", "city", "../bad"}, {"config", "set", "wrong", "value"}, {"config", "set", "city", ""}} {
		if _, err := execute(t, p, "unused", args...); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
	after, _ := os.ReadFile(p.config)
	if !bytes.Equal(before, after) {
		t.Error("invalid update replaced valid config")
	}
	info, err := os.Stat(p.config)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("config permissions %v", info.Mode())
	}

}

func TestInvalidInputsMakeNoRequests(t *testing.T) {
	p := tempPaths(t)
	for _, args := range [][]string{
		{}, {"id", "2467"}, {"search"}, {"search", "--city", "../bad", "x"}, {"search", "--page", "0", "x"},
		{"search", "--deadline=-2", "x"}, {"search", "--type", "bad", "x"}, {"search", "--city", "kazan", "--timeout=0s", "x"},
		{"search", "--city", "kazan", "--user-agent", "\n", "x"}, {"search", "x"}, {"show", "2467", "other"}, {"show", "https://evil.example/2467"},
		{"show", "--city", "kazan", "--url", "https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/", "2468"},
		{"show", "--type", "complexes", "https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/"},
		{"search", "--type", "checkups", "--deadline=1", "x"}, {"auth"}, {"auth", "status"}, {"auth", "import", "--browser", "chrome"},
	} {
		if out, err := execute(t, p, "http://127.0.0.1:1", args...); err == nil || out != "" || strings.Contains(err.Error(), "connection refused") {
			t.Errorf("args=%v out=%q err=%v", args, out, err)
		}
	}
}

func TestShowURLCityPrecedenceAndFlags(t *testing.T) {
	for _, args := range [][]string{
		{"show", "https://www.invitro.ru/analizes/for-doctors/kazan/629/2467/?utm_source=test#details"},
		{"show", "2467", "--city", "kazan", "--full"},
		{"show", "--city", "kazan", "--url", "https://www.invitro.ru/analizes/for-doctors/629/2467/", "2467"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			p := tempPaths(t)
			if err := atomicJSON(p.config, userConfig{City: "moscow"}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/golk/addresses/api/v1/cities":
					if r.URL.Query().Get("q") != "kazan" {
						t.Error("wrong city")
					}
					writeFixture(t, w, "city")
				case "/golk/tests/api/v1/mappers/product/2467":
					writeFixture(t, w, "mapper")
				default:
					writeFixture(t, w, "detail")
				}
			}))
			defer server.Close()
			output, err := execute(t, p, server.URL, args...)
			if err != nil {
				t.Fatal(err)
			}
			var result detailResult
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.City != "kazan" || result.ID != "2467" {
				t.Errorf("bad output: %s", output)
			}
			for _, id := range []string{"2467", testProductID} {
				entry, err := p.lookup("kazan", id)
				if err != nil || entry == nil || entry.Type != "analyses" {
					t.Fatalf("cache %q: %+v %v", id, entry, err)
				}
			}
		})
	}
}

func TestSavedCityAndComplexType(t *testing.T) {
	p := tempPaths(t)
	if err := atomicJSON(p.config, userConfig{City: "kazan"}); err != nil {
		t.Fatal(err)
	}
	var raw apiProduct
	if err := json.Unmarshal(fixture(t, "complex"), &raw); err != nil {
		t.Fatal(err)
	}
	item, err := normalizeProduct(raw, "kazan", Complexes())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.remember("kazan", []product{item}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/golk/addresses/api/v1/cities":
			writeFixture(t, w, "city")
		case "/golk/tests/api/v1/complexes/" + raw.ID:
			writeFixture(t, w, "complex")
		default:
			t.Errorf("wrong type routing: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output, err := execute(t, p, server.URL, "show", raw.ID)
	if err != nil || !strings.Contains(output, `"type": "complexes"`) {
		t.Fatalf("output=%s error=%v", output, err)
	}
}

func TestMalformedConfigFails(t *testing.T) {
	p := tempPaths(t)
	for _, data := range []string{`null`, `[]`, `{"city":"kazan"} {}`, `{"city":"../bad"}`, `{"unknown":1}`, `{"city":42}`} {
		if err := os.WriteFile(p.config, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, p, "unused", "config"); err == nil {
			t.Errorf("accepted config %s", data)
		}
	}
}

func TestSearchOptionsAfterQueryAndLiteralSeparator(t *testing.T) {
	p := tempPaths(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "cities") {
			writeFixture(t, w, "city")
			return
		}
		if r.URL.Query().Get("q") != "ттг --literal" {
			t.Error("option parser changed query")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"group":"analyses","ids":[]}`)); err != nil {
			t.Errorf("write search response: %v", err)
		}
	}))
	defer server.Close()
	if _, err := execute(t, p, server.URL, "search", "ттг", "--city", "kazan", "--", "--literal"); err != nil {
		t.Fatal(err)
	}
}
