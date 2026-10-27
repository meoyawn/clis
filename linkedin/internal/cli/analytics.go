package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
)

func runAnalytics(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		return writeHelp(stdout, []string{"analytics"})
	}
	command := args[0]
	if command != "summary" && command != "compare" {
		return fmt.Errorf("unknown analytics command %q; expected summary or compare", command)
	}
	fs := flag.NewFlagSet("analytics "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _ = writeHelp(stderr, []string{"analytics", command}) }
	var save, saveResponse string
	if command == "summary" {
		fs.StringVar(&save, "save", "", "also save this snapshot to a new private JSON file")
		fs.StringVar(&saveResponse, "save-response", "", "save the raw dashboard to a new private file")
	}
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"analytics", command})
	}
	if err != nil {
		return err
	}
	if command == "compare" {
		if len(positionals) != 2 {
			return fmt.Errorf("analytics compare requires BEFORE.json and AFTER.json; got %d arguments", len(positionals))
		}
		result, err := compareAnalyticsFiles(positionals[0], positionals[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if len(positionals) != 0 {
		return fmt.Errorf("analytics summary takes no positional arguments; got %d", len(positionals))
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	var capture io.Writer
	if saveResponse != "" {
		f, err := os.OpenFile(saveResponse, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create analytics capture %q: %w", saveResponse, err)
		}
		defer f.Close()
		capture = f
	}
	result, err := client.analytics(ctx, capture)
	if err != nil {
		return err
	}
	if save != "" {
		f, err := os.OpenFile(save, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create analytics snapshot %q: %w", save, err)
		}
		writeErr := writeJSON(f, result)
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return fmt.Errorf("save analytics snapshot: %w", err)
		}
	}
	return writeJSON(stdout, result)
}

func (c *linkedinClient) analytics(ctx context.Context, capture io.Writer) (domain.AnalyticsSnapshot, error) {
	self, err := c.mailbox(ctx)
	if err != nil {
		return domain.AnalyticsSnapshot{}, err
	}
	owner, err := c.profile(ctx, profileRef{ID: strings.TrimPrefix(self, "urn:li:fsd_profile:")})
	if err != nil {
		return domain.AnalyticsSnapshot{}, err
	}
	if owner.ProfileURN != self {
		return domain.AnalyticsSnapshot{}, fmt.Errorf("analytics owner does not match signed-in profile")
	}
	resp, err := c.get(ctx, "/dashboard/", http.Header{"Accept": {"text/html"}, "Accept-Language": {"en-US,en;q=0.9"}})
	if err != nil {
		return domain.AnalyticsSnapshot{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return domain.AnalyticsSnapshot{}, fmt.Errorf("read analytics dashboard: %w", err)
	}
	if len(data) > 16<<20 {
		return domain.AnalyticsSnapshot{}, fmt.Errorf("analytics dashboard exceeds 16 MiB")
	}
	if capture != nil {
		if _, err := capture.Write(data); err != nil {
			return domain.AnalyticsSnapshot{}, fmt.Errorf("save analytics capture: %w", err)
		}
	}
	result, err := service.ParseAnalyticsDashboard(string(data), owner.URL, owner.Name)
	if err != nil {
		return domain.AnalyticsSnapshot{}, err
	}
	result.FetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return result, nil
}

type analyticsChangeResult struct {
	Before      *domain.AnalyticsMetric `json:"before"`
	After       *domain.AnalyticsMetric `json:"after"`
	CountChange *int64                  `json:"count_change"`
	Note        string                  `json:"note"`
}

type analyticsComparison struct {
	ProfileURL        string                `json:"profile_url"`
	BeforeAt          string                `json:"before_at"`
	AfterAt           string                `json:"after_at"`
	ProfileViews      analyticsChangeResult `json:"profile_views"`
	PostImpressions   analyticsChangeResult `json:"post_impressions"`
	SearchAppearances analyticsChangeResult `json:"search_appearances"`
	Followers         analyticsChangeResult `json:"followers"`
}

func compareAnalyticsFiles(beforePath, afterPath string) (analyticsComparison, error) {
	read := func(path string) (domain.AnalyticsSnapshot, error) {
		f, err := os.Open(path)
		if err != nil {
			return domain.AnalyticsSnapshot{}, fmt.Errorf("open snapshot %q: %w", path, err)
		}
		defer f.Close()
		var snapshot domain.AnalyticsSnapshot
		if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&snapshot); err != nil {
			return snapshot, fmt.Errorf("decode snapshot %q: %w", path, err)
		}
		if snapshot.SchemaVersion != 1 || snapshot.ProfileURL == "" {
			return snapshot, fmt.Errorf("unsupported or incomplete analytics snapshot %q", path)
		}
		return snapshot, nil
	}
	before, err := read(beforePath)
	if err != nil {
		return analyticsComparison{}, err
	}
	after, err := read(afterPath)
	if err != nil {
		return analyticsComparison{}, err
	}
	beforeAt, err := time.Parse(time.RFC3339Nano, before.FetchedAt)
	if err != nil {
		return analyticsComparison{}, fmt.Errorf("invalid before snapshot timestamp %q", before.FetchedAt)
	}
	afterAt, err := time.Parse(time.RFC3339Nano, after.FetchedAt)
	if err != nil || !afterAt.After(beforeAt) {
		return analyticsComparison{}, fmt.Errorf("after snapshot timestamp %q must be valid and later than %q", after.FetchedAt, before.FetchedAt)
	}
	if before.ProfileURL != after.ProfileURL {
		return analyticsComparison{}, fmt.Errorf("analytics snapshots have different owners %q and %q", before.ProfileURL, after.ProfileURL)
	}
	compare := func(a, b *domain.AnalyticsMetric) analyticsChangeResult {
		r := analyticsChangeResult{Before: a, After: b}
		switch {
		case a == nil || b == nil:
			r.Note = "Metric unavailable in one or both snapshots."
		case a.Count == nil || b.Count == nil:
			r.Note = "Rounded or unavailable count; no exact change calculated."
		case a.Window != b.Window:
			r.Note = "Reporting windows differ; inspect the original periods before comparing."
		default:
			delta := *b.Count - *a.Count
			r.CountChange = &delta
			r.Note = "Difference in reported totals; overlapping rolling windows are not new events or proof of post attribution."
		}
		return r
	}
	return analyticsComparison{ProfileURL: before.ProfileURL, BeforeAt: before.FetchedAt, AfterAt: after.FetchedAt,
		ProfileViews: compare(before.ProfileViews, after.ProfileViews), PostImpressions: compare(before.PostImpressions, after.PostImpressions), SearchAppearances: compare(before.SearchAppearances, after.SearchAppearances), Followers: compare(before.Followers, after.Followers)}, nil
}

const analyticsHelp = `Read your private LinkedIn analytics and compare local snapshots.

Usage:
  linkedin analytics summary [--save NEW.json] [--save-response NEW.html]
  linkedin analytics compare BEFORE.json AFTER.json

Summary reads the signed-in account's dashboard with three sequential GETs.
It returns profile viewers, post impressions, search appearances, and followers
when present, with the displayed windows, comparison text, source URLs and UTC
fetch time. Missing or rounded counts are never replaced with exact zeroes.
--save writes a new 0600 JSON snapshot; --save-response captures raw private HTML
for debugging. Files are never overwritten. Default output is JSON, with no files.

Compare is local-only and needs no authentication. It verifies owner and time
ordering. A count change is calculated only for exact values with matching
reporting-window labels. Overlapping rolling totals do not measure new events,
and these aggregate metrics cannot attribute an interview or visit to one post.
No posting, reactions, profile changes, or other account mutations occur.
`
