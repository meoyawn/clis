package service

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Optional replay of local private captures. The captures stay outside
// the repository; only parsed public fields are inspected, never logged.
func TestPrivateHARs(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("OZON_HAR_DIR")
	if dir == "" {
		t.Skip("set OZON_HAR_DIR to replay local ozon-*.har captures")
	}
	var searchPages, filtersPages int
	var product *ShowResult
	for _, name := range []string{"search", "filters", "show"} {
		// #nosec G304 G703 -- The opt-in replay explicitly reads captures from the local OZON_HAR_DIR.
		b, e := os.ReadFile(filepath.Join(dir, "ozon-"+name+".har"))
		if e != nil {
			t.Fatal(e)
		}
		var h struct {
			Log struct {
				Entries []struct {
					Request struct {
						URL string `json:"url"`
					} `json:"request"`
					Response struct {
						Status  int `json:"status"`
						Content struct {
							Text     string `json:"text"`
							Encoding string `json:"encoding"`
							MimeType string `json:"mimeType"`
						} `json:"content"`
					} `json:"response"`
				} `json:"entries"`
			} `json:"log"`
		}
		if e = json.Unmarshal(b, &h); e != nil {
			t.Fatal(e)
		}
		for i, entry := range h.Log.Entries {
			if entry.Response.Status != 200 || entry.Response.Content.Text == "" {
				continue
			}
			body := []byte(entry.Response.Content.Text)
			if entry.Response.Content.Encoding == "base64" {
				body, e = base64.StdEncoding.DecodeString(string(body))
				if e != nil {
					t.Fatal(e)
				}
			}
			requestURL, e := url.Parse(entry.Request.URL)
			if e != nil {
				t.Fatal(e)
			}
			if name == "show" && strings.HasPrefix(requestURL.Path, "/product/") && strings.Contains(entry.Response.Content.MimeType, "html") {
				p, e := readPageHTML(strings.NewReader(string(body)))
				if e != nil {
					t.Fatal(e)
				}
				ref, e := ParseItemReference(entry.Request.URL)
				if e != nil {
					t.Fatal(e)
				}
				product, e = parseShow(p, ref)
				if e != nil {
					t.Fatalf("show entry %d: %v", i, e)
				}
				if product.Prices.WithOzonCard == nil || product.Prices.WithoutOzonCard == nil || len(product.Images) < 1 || len(product.Variants) < 1 || product.Quantity == nil {
					t.Fatal("captured product lost required fields")
				}
			}
			if requestURL.Path != entrypointPath {
				continue
			}
			var p page
			if json.Unmarshal(body, &p) != nil {
				continue
			}
			inner := requestURL.Query().Get("url")
			innerURL, e := url.Parse(inner)
			if e != nil {
				t.Fatal(e)
			}
			if innerURL.Path == "/search/" {
				query := innerURL.Query().Get("text")
				o := SearchOptions{Query: query, Page: sourcePageNumber(inner)}
				result, e := parseSearch(&p, o, inner)
				if e != nil {
					t.Fatalf("%s entry %d: %v", name, i, e)
				}
				if result.Count == 0 {
					t.Fatalf("%s entry %d unexpectedly empty", name, i)
				}
				for _, item := range result.Results {
					if len(item.Images) == 0 || item.Price == nil {
						t.Fatalf("%s entry %d lost public card fields", name, i)
					}
				}
				searchPages++
				if w, err := p.first("filtersDesktop"); err != nil {
					t.Fatal(err)
				} else if w != nil {
					r, e := parseFilters(&p, query)
					if e != nil || len(r.Filters) == 0 {
						t.Fatalf("filters entry %d: %v", i, e)
					}
					filtersPages++
				}
			}
			if name == "show" && innerURL.Query().Get("layout_container") == "pdpPage2column" && product != nil {
				if e = parseDetails(&p, product); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	if searchPages < 10 || filtersPages < 3 || product == nil || product.Description == "" || len(product.Characteristics) == 0 {
		t.Fatalf("incomplete capture replay: search=%d filters=%d", searchPages, filtersPages)
	}
	t.Logf("replayed %d search pages, %d filter pages and product/details captures", searchPages, filtersPages)
}
