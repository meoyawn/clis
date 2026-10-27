package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

// Comparing snapshots is a user-facing local operation. Differing periods and
// account identities must never turn into a claimed improvement from posting.
func TestAnalyticsComparisonUsesOwnerTimeAndPeriods(t *testing.T) {
	for _, tc := range []struct {
		name, owner, afterAt, window, wantError string
		rounded                                 bool
		wantDelta                               bool
	}{
		{name: "matching rolling labels", wantDelta: true},
		{name: "different dated periods", window: "Oct 20–26"},
		{name: "rounded after", rounded: true},
		{name: "different owner", owner: "https://www.linkedin.com/in/other/", wantError: "different owners"},
		{name: "reversed chronology", afterAt: "2026-10-26T10:00:00Z", wantError: "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeCount, afterCount := int64(2), int64(5)
			before := domain.AnalyticsSnapshot{SchemaVersion: 1, ProfileURL: postAuthorURL, FetchedAt: "2026-10-27T10:00:00Z", SearchAppearances: &domain.AnalyticsMetric{Count: &beforeCount, Window: "Oct 13–19"}}
			after := domain.AnalyticsSnapshot{SchemaVersion: 1, ProfileURL: postAuthorURL, FetchedAt: "2026-10-28T10:00:00Z", SearchAppearances: &domain.AnalyticsMetric{Count: &afterCount, Window: "Oct 13–19"}}
			if tc.owner != "" {
				after.ProfileURL = tc.owner
			}
			if tc.afterAt != "" {
				after.FetchedAt = tc.afterAt
			}
			if tc.window != "" {
				after.SearchAppearances.Window = tc.window
			}
			if tc.rounded {
				after.SearchAppearances.Count = nil
			}
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "before.json"), filepath.Join(dir, "after.json")}
			for i, value := range []domain.AnalyticsSnapshot{before, after} {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths[i], data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := runAnalytics(context.Background(), append([]string{"compare"}, paths...), authStore{dir: filepath.Join(dir, "no-auth")}, &out, io.Discard)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || out.Len() != 0 {
					t.Fatalf("expected %q, output=%q, err=%v", tc.wantError, out.String(), err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got analyticsComparison
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if (got.SearchAppearances.CountChange != nil) != tc.wantDelta || (tc.wantDelta && *got.SearchAppearances.CountChange != 3) || got.SearchAppearances.Note == "" || got.ProfileViews.CountChange != nil {
				t.Fatalf("misleading comparison: %+v", got)
			}
		})
	}
}
