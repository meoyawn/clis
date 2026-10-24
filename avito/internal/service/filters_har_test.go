package service

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Optional local replay. Only public schema fields are read; request headers,
// cookies, and account data are never logged or copied into fixtures.
func TestPrivateFilterHAR(t *testing.T) {
	t.Parallel()

	path := os.Getenv("AVITO_FILTERS_HAR")
	if path == "" {
		t.Skip("set AVITO_FILTERS_HAR to a private local capture")
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304/G703: the caller explicitly selects a private local capture to replay.
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Log struct {
			Entries []struct {
				Request  struct{ URL string } `json:"request"`
				Response struct {
					Status  int                             `json:"status"`
					Content struct{ Text, Encoding string } `json:"content"`
				} `json:"response"`
			} `json:"entries"`
		} `json:"log"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	indexed := regexp.MustCompile(`^(params\[[0-9]+\])\[[0-9]+\]$`)
	forms, suggestions := 0, 0
	for i, entry := range capture.Log.Entries {
		u, err := url.Parse(entry.Request.URL)
		if err != nil || u.Host != "www.avito.ru" || entry.Response.Status != 200 || (u.Path != "/web/1/js/items" && u.Path != filterSuggestPath) {
			continue
		}
		q := u.Query()
		if q.Get("countOnly") == "1" {
			continue
		}
		body := []byte(entry.Response.Content.Text)
		if entry.Response.Content.Encoding == "base64" {
			body, err = base64.StdEncoding.DecodeString(entry.Response.Content.Text)
			if err != nil {
				t.Fatalf("entry %d: invalid base64", i)
			}
		}
		o := SearchOptions{Query: q.Get("name"), Page: 1, Filters: url.Values{}, LocalPriority: q.Get("localPriority") == "1"}
		o.LocationID, err = strconv.ParseInt(q.Get("locationId"), 10, 64)
		if err != nil {
			t.Fatalf("entry %d: invalid location ID: %v", i, err)
		}
		if category := q.Get("categoryId"); category != "" {
			o.CategoryID, err = strconv.ParseInt(category, 10, 64)
			if err != nil {
				t.Fatalf("entry %d: invalid category ID: %v", i, err)
			}
		}
		if page := q.Get("p"); page != "" {
			o.Page, err = strconv.Atoi(page)
			if err != nil {
				t.Fatalf("entry %d: invalid page: %v", i, err)
			}
		}
		if sort := q.Get("s"); sort != "" {
			o.Sort, err = NewSortFromValue(sort)
			if err != nil {
				t.Fatalf("entry %d: %v", i, err)
			}
		}
		for key, values := range q {
			switch {
			case strings.HasPrefix(key, "params["):
				if match := indexed.FindStringSubmatch(key); match != nil {
					key = match[1]
				}
				o.Filters[key] = append(o.Filters[key], values...)
			case key == "pmin" || key == "pmax":
				o.Filters[key] = values
			case key == "user":
				o.Filters["owner"] = values
			}
		}
		if u.Path == filterSuggestPath {
			offset := 0
			if raw := q.Get("offset"); raw != "" {
				offset, err = strconv.Atoi(raw)
				if err != nil {
					t.Fatalf("entry %d: invalid offset: %v", i, err)
				}
			}
			_, err = ParseFilterValues(body, FilterValuesOptions{Search: o, Key: "params[" + q.Get("paramId") + "]", ValueQuery: q.Get("query"), Offset: offset})
			suggestions++
		} else {
			if _, err = ParseFilters(body, o); err == nil {
				_, err = ParseSearch(body, o)
			}
			forms++
		}
		if err != nil {
			t.Fatalf("entry %d (%s): %v", i, u.Path, err)
		}
	}
	if forms == 0 || suggestions == 0 {
		t.Fatalf("capture needs both filter forms and suggestions, got %d forms and %d suggestions", forms, suggestions)
	}
	t.Logf("Validated %d search/filter responses and %d suggestion responses", forms, suggestions)
}
