package domain

// AnalyticsMetric keeps LinkedIn's displayed reporting window and comparison.
// Count is null for rounded values such as 1.2K; it is never invented as zero.
type AnalyticsMetric struct {
	Count        *int64 `json:"count"`
	DisplayCount string `json:"display_count"`
	Window       string `json:"window"`
	ChangeText   string `json:"change_text"`
	Evidence     string `json:"evidence"`
}

type AnalyticsSnapshot struct {
	ProfileURL        string           `json:"profile_url"`
	FetchedAt         string           `json:"fetched_at"`
	ProfileViews      *AnalyticsMetric `json:"profile_views"`
	PostImpressions   *AnalyticsMetric `json:"post_impressions"`
	SearchAppearances *AnalyticsMetric `json:"search_appearances"`
	Followers         *AnalyticsMetric `json:"followers"`
}
