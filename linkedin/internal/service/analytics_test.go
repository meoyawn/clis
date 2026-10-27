package service

import (
	"strings"
	"testing"
)

const analyticsDocument = `<html><body>
<aside><a href="https://www.linkedin.com/in/example-author/">Example Author</a>
<a href="/analytics/profile-views/">999 Profile viewers in 90 days</a></aside>
<section aria-label="Primary content">
<a href="/analytics/creator/content/"><span>0</span><span>Post impressions</span><span>in 7 days</span><span>0% vs. prior 7 days</span></a>
<a href="/analytics/profile-views/">32 Profile viewers in 90 days 75% vs. prior 7 days</a>
<a href="/analytics/search-appearances/">2 Search appearances Oct 13–19 0% vs. Oct 6–12</a>
<a href="/analytics/creator/audience/">165 Total followers 3% vs. prior 7 days</a>
<div hidden><a href="/analytics/creator/content/">999 Post impressions in 7 days</a></div>
</section></body></html>`

// This is the parser's primary contract test: retain the source periods, ignore
// unrelated cards, and distinguish unavailable/rounded counts from exact zero.
func TestAnalyticsDashboardEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement, owner, wantError string
	}{
		{name: "exact values and separate periods"},
		{name: "rounded count", old: "32 Profile viewers", replacement: "1.2K Profile viewers"},
		{name: "missing is not zero", old: `href="/analytics/search-appearances/"`, replacement: `href="/unsupported/"`, wantError: "missing"},
		{name: "duplicate is ambiguous", old: "</section>", replacement: `<a href="/analytics/profile-views/">33 Profile viewers in 90 days</a></section>`, wantError: "duplicate"},
		{name: "wrong owner", owner: "https://www.linkedin.com/in/other/", wantError: "owner"},
		{name: "wrong label", old: "32 Profile viewers", replacement: "32 Profile impressions", wantError: "unrecognized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := analyticsDocument
			if tc.old != "" {
				document = strings.ReplaceAll(document, tc.old, tc.replacement)
			}
			owner := tc.owner
			if owner == "" {
				owner = "https://www.linkedin.com/in/example-author/"
			}
			got, err := ParseAnalyticsDashboard(document, owner, "Example Author")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected %q, got %+v, %v", tc.wantError, got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.PostImpressions.Count == nil || *got.PostImpressions.Count != 0 || got.PostImpressions.Window != "in 7 days" || got.SearchAppearances.Window != "Oct 13–19" || *got.SearchAppearances.Count != 2 || *got.Followers.Count != 165 {
				t.Fatalf("lost count or reporting period: %+v", got)
			}
			if tc.name == "rounded count" {
				if got.ProfileViews.Count != nil || got.ProfileViews.DisplayCount != "1.2K" {
					t.Fatalf("rounded count presented as exact: %+v", got.ProfileViews)
				}
			} else if got.ProfileViews.Count == nil || *got.ProfileViews.Count != 32 {
				t.Fatalf("wrong profile views: %+v", got.ProfileViews)
			}
			if got.ProfileViews.Window != "in 90 days" || got.ProfileViews.ChangeText != "75% vs. prior 7 days" || got.ProfileViews.Evidence == "" || got.ProfileViews.SourceURL != "https://www.linkedin.com/analytics/profile-views/" {
				t.Fatalf("lost source evidence: %+v", got.ProfileViews)
			}
		})
	}
}
