package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"golang.org/x/net/html"
)

const AnalyticsDashboardURL = "https://www.linkedin.com/dashboard/"

var analyticsCardValue = regexp.MustCompile(`^([0-9][0-9,.]*(?:[KMBkmb])?)\s+(Post impressions|Profile viewers|Search appearances|Total followers)(?:\s+(.*))?$`)
var analyticsChange = regexp.MustCompile(`^(.*?)\s*([+−-]?[0-9,.]+%\s+vs\..*)$`)

// ParseAnalyticsDashboard follows the authenticated dashboard's observed cards.
// No viewer identities, inferred time windows or synthetic zeroes are exported.
func ParseAnalyticsDashboard(document, profileURL, name string) (domain.AnalyticsSnapshot, error) {
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return domain.AnalyticsSnapshot{}, fmt.Errorf("parse analytics dashboard: %w", err)
	}
	var primary *html.Node
	var owner bool
	for n := range root.Descendants() {
		if n.Type != html.ElementNode || postHidden(n) {
			continue
		}
		if postAttr(n, "aria-label") == "Primary content" {
			if primary != nil {
				return domain.AnalyticsSnapshot{}, fmt.Errorf("analytics dashboard has multiple primary regions")
			}
			primary = n
		}
		if n.Data == "a" && strings.TrimSuffix(postAttr(n, "href"), "/") == strings.TrimSuffix(profileURL, "/") && strings.Contains(analyticsText(n), name) {
			owner = true
		}
	}
	if primary == nil || !owner {
		return domain.AnalyticsSnapshot{}, fmt.Errorf("analytics dashboard does not confirm signed-in owner %q and primary content", profileURL)
	}
	result := domain.AnalyticsSnapshot{SchemaVersion: 1, ProfileURL: profileURL, SourceURL: AnalyticsDashboardURL, Notes: []string{
		"Metrics retain LinkedIn's own reporting windows; profile views, post impressions and search appearances can cover different periods.",
		"Aggregate profile metrics do not attribute a visit, search appearance or interview to an individual post.",
		"Snapshot differences compare reported totals, not new events; rolling windows overlap and search reporting can lag.",
	}}
	for n := range primary.Descendants() {
		if n.Type != html.ElementNode || n.Data != "a" || postHidden(n) {
			continue
		}
		u, err := url.Parse(postAttr(n, "href"))
		if err != nil || u.User != nil || (u.Host != "" && u.Host != "www.linkedin.com") || (u.Scheme != "" && u.Scheme != "https") {
			continue
		}
		var slot **domain.AnalyticsMetric
		var expectedLabel string
		switch u.Path {
		case "/analytics/creator/content/":
			slot, expectedLabel = &result.PostImpressions, "Post impressions"
		case "/analytics/profile-views/":
			slot, expectedLabel = &result.ProfileViews, "Profile viewers"
		case "/analytics/search-appearances/":
			slot, expectedLabel = &result.SearchAppearances, "Search appearances"
		case "/analytics/creator/audience/":
			slot, expectedLabel = &result.Followers, "Total followers"
		default:
			continue
		}
		text := analyticsText(n)
		match := analyticsCardValue.FindStringSubmatch(text)
		if match == nil || match[2] != expectedLabel {
			return domain.AnalyticsSnapshot{}, fmt.Errorf("unrecognized %s analytics card %q", expectedLabel, text)
		}
		if *slot != nil {
			return domain.AnalyticsSnapshot{}, fmt.Errorf("duplicate %s analytics cards", expectedLabel)
		}
		metric := &domain.AnalyticsMetric{DisplayCount: match[1], Label: expectedLabel, Window: match[3], Evidence: text, SourceURL: "https://www.linkedin.com" + u.Path}
		if count, err := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64); err == nil {
			metric.Count = &count
		}
		if parts := analyticsChange.FindStringSubmatch(match[3]); parts != nil {
			metric.Window, metric.ChangeText = strings.TrimSpace(parts[1]), parts[2]
		}
		*slot = metric
	}
	if result.ProfileViews == nil || result.PostImpressions == nil || result.SearchAppearances == nil {
		return domain.AnalyticsSnapshot{}, fmt.Errorf("analytics dashboard is missing one or more requested metrics; unavailable data is not zero")
	}
	return result, nil
}

func analyticsText(n *html.Node) string {
	var chunks []string
	for child := range n.Descendants() {
		if child.Type == html.TextNode && !postHidden(child) {
			chunks = append(chunks, child.Data)
		}
	}
	return strings.Join(strings.Fields(strings.Join(chunks, " ")), " ")
}
