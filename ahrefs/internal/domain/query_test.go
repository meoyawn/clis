package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestDataExplorerTargetPreservesCrawlFilterAndColumns(t *testing.T) {
	q, err := ParseTarget("https://app.ahrefs.com/site-audit/123/data-explorer?columns=pageRating%2Curl%2CogTagsValue&current=21-01-2026T120000&filterCollapsed=true&filterId=synthetic-filter&issueId=synthetic-og")
	if err != nil {
		t.Fatal(err)
	}
	want := Query{ProjectID: "123", Crawl: "2026-01-21T12:00:00Z", IssueID: "synthetic-og", FilterID: "synthetic-filter", Columns: []string{"pageRating", "url", "ogTagsValue"}}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("target = %+v; want %+v", q, want)
	}
	issues, err := ParseTarget("https://app.ahrefs.com/site-audit/123/issues?current=2026-01-21T15%3A00%3A00%2B03%3A00")
	if err != nil || issues.Crawl != q.Crawl || issues.ProjectID != q.ProjectID {
		t.Fatalf("issues target = %+v, %v", issues, err)
	}
}

func TestTargetRejectsAmbiguousOrUnsupportedViews(t *testing.T) {
	for _, test := range []struct{ input, message string }{
		{"https://app.ahrefs.com.other.test/site-audit/123/issues", "invalid target"},
		{"http://app.ahrefs.com/site-audit/123/issues", "invalid target"},
		{"https://app.ahrefs.com/site-audit/123/issues?segment=unknown", "unsupported URL parameter"},
		{"https://app.ahrefs.com/site-audit/123/issues?current=latest&current=21-01-2026T120000", "exactly once"},
		{"https://app.ahrefs.com/site-audit/123/issues?current=31-02-2026T120000", "invalid crawl timestamp"},
		{"https://app.ahrefs.com/site-audit/123/data-explorer?columns=url,url", "duplicate column"},
	} {
		t.Run(test.input, func(t *testing.T) {
			_, err := ParseTarget(test.input)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v; want %q", err, test.message)
			}
		})
	}
}
