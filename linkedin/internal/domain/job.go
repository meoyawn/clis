package domain

// Job contains the public fields supplied by a LinkedIn search result card.
type Job struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Company  string `json:"company"`
	Location string `json:"location"`
	Details  string `json:"details,omitempty"`
	ListedAt string `json:"listed_at,omitempty"`
}
