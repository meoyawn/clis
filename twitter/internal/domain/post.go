package domain

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var numericID = regexp.MustCompile(`^[1-9][0-9]*$`)
var handlePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

func Handle(value string) (string, error) {
	value = strings.TrimPrefix(value, "@")
	if !handlePattern.MatchString(value) {
		return "", fmt.Errorf("invalid handle %q; expected 1–15 letters, digits or underscores", value)
	}
	return value, nil
}

type PostRef struct{ id string }

func NewPostRef(value string) (PostRef, error) {
	if numericID.MatchString(value) {
		return PostRef{id: value}, nil
	}
	u, err := url.Parse(value)
	if err == nil && u.Scheme == "https" && u.User == nil && (u.Host == "x.com" || u.Host == "www.x.com" || u.Host == "twitter.com") && u.RawQuery == "" && u.Fragment == "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) == 3 && parts[1] == "status" && numericID.MatchString(parts[2]) {
			if _, err := Handle(parts[0]); err == nil {
				return PostRef{id: parts[2]}, nil
			}
		}
	}
	return PostRef{}, fmt.Errorf("invalid post reference %q; expected numeric ID or https://x.com/HANDLE/status/ID without tracking parameters", value)
}

func (r PostRef) ID() string { return r.id }

type Draft struct {
	text  string
	upper int
}

func NewDraft(text string) (Draft, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return Draft{}, fmt.Errorf("post text must be nonempty valid UTF-8")
	}
	// Conservative twitter-text v3 bound. Counting URLs before shortening and
	// emoji code points separately may reject valid long posts, never truncate.
	upper := 0
	for _, r := range text {
		if r == 0 || r == 0xfffe || r == 0xffff || (r < 32 && r != '\n' && r != '\t' && r != '\r') {
			return Draft{}, fmt.Errorf("post text contains unsupported control character U+%04X", r)
		}
		if r <= 4351 || (r >= 8192 && r <= 8205) || (r >= 8208 && r <= 8223) || (r >= 8242 && r <= 8247) {
			upper++
		} else {
			upper += 2
		}
	}
	if upper > 280 {
		return Draft{}, fmt.Errorf("post length upper bound is %d; this CLI supports at most 280 (URLs before shortening and complex emoji may be overcounted); shorten the text or split it into reviewed replies", upper)
	}
	return Draft{text: text, upper: upper}, nil
}

func (d Draft) Text() string          { return d.text }
func (d Draft) LengthUpperBound() int { return d.upper }
func (d Draft) SHA256() string        { return fmt.Sprintf("%x", sha256.Sum256([]byte(d.text))) }

type Profile struct {
	ID        string `json:"id"`
	Handle    string `json:"handle"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Followers int64  `json:"followers"`
}

type Post struct {
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Author    Profile  `json:"author"`
	Text      string   `json:"text"`
	CreatedAt string   `json:"created_at"`
	ReplyTo   string   `json:"reply_to,omitempty"`
	MediaIDs  []string `json:"media_ids"`
	ImageURLs []string `json:"image_urls"`
	Views     *int64   `json:"views"`
	Likes     int64    `json:"likes"`
	Replies   int64    `json:"replies"`
	Reposts   int64    `json:"reposts"`
	Quotes    int64    `json:"quotes"`
	Bookmarks int64    `json:"bookmarks"`
}

type Conversation struct {
	Post            Post   `json:"post"`
	Parents         []Post `json:"parents"`
	Replies         []Post `json:"replies"`
	ParentsComplete bool   `json:"parents_complete"`
	MissingParentID string `json:"missing_parent_id,omitempty"`
}
