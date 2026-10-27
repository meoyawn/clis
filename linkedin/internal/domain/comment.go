package domain

type Comment struct {
	ID        string `json:"id"`
	URN       string `json:"urn"`
	ParentURN string `json:"parent_urn"`
	URL       string `json:"url"`
	AuthorURL string `json:"author_url"`
	Text      string `json:"text"`
}
