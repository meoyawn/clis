package cli

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// LinkedIn has served the hydration array both as JSON and as a JavaScript array of
// single-quoted strings; both must yield the same records.
func TestHydrationArrayReadsJSONAndSingleQuotedJavaScriptAlike(t *testing.T) {
	read := func(array string) documentData {
		t.Helper()
		doc, err := html.Parse(strings.NewReader(`<html><script>window.__como_rehydration__ = ` + array + `;</script></html>`))
		if err != nil {
			t.Fatal(err)
		}
		return readDocumentData(doc)
	}
	jsonArray := `["0:{\"name\":\"Kafka's \\\"broker\\\" é 🚀\"}\n", "1:[\"tail\"]\n"]`
	jsArray := `[ '0:{"name":"Kafka\'s \\"broker\\" \xe9 🚀"}\n', '1:["tail"]\n' ]`
	want := read(jsonArray)
	if len(want) != 2 {
		t.Fatalf("JSON array gave %d records, want 2: %#v", len(want), want)
	}
	if got := read(jsArray); !reflect.DeepEqual(got, want) {
		t.Fatalf("single-quoted array gave %#v, want %#v", got, want)
	}
}
