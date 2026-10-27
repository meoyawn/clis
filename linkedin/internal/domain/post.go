package domain

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// PostDraft preserves the exact text reviewed by the author.
type PostDraft struct{ text string }

func NewPostDraft(text string) (PostDraft, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return PostDraft{}, fmt.Errorf("post text must be nonempty valid UTF-8")
	}
	units := len(utf16.Encode([]rune(text)))
	if units > 3000 {
		return PostDraft{}, fmt.Errorf("post text uses %d UTF-16 code units; maximum is 3000", units)
	}
	return PostDraft{text: text}, nil
}

func (p PostDraft) Text() string   { return p.text }
func (p PostDraft) SHA256() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(p.text))) }

var activityPattern = regexp.MustCompile(`^urn:li:activity:[1-9][0-9]*$`)

// PostRef accepts an exact activity URN or LinkedIn's canonical feed permalink.
type PostRef struct{ urn string }

func NewPostRef(value string) (PostRef, error) {
	urn := value
	if !strings.HasPrefix(value, "urn:") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host != "www.linkedin.com" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return PostRef{}, fmt.Errorf("invalid post URL %q: expected an https://www.linkedin.com/feed/update/urn:li:activity:<id>/ permalink without tracking parameters", value)
		}
		var ok bool
		urn, ok = strings.CutPrefix(strings.TrimSuffix(u.Path, "/"), "/feed/update/")
		if !ok {
			return PostRef{}, fmt.Errorf("invalid post URL %q: expected /feed/update/urn:li:activity:<id>/", value)
		}
	}
	if !activityPattern.MatchString(urn) {
		return PostRef{}, fmt.Errorf("invalid post %q: expected a positive activity URN or its LinkedIn feed permalink", value)
	}
	return PostRef{urn: urn}, nil
}

func (p PostRef) URN() string { return p.urn }
func (p PostRef) URL() string { return "https://www.linkedin.com/feed/update/" + p.urn + "/" }

// Post is a post independently read from its own permalink.
type Post struct {
	URL           string   `json:"url"`
	URN           string   `json:"urn"`
	AuthorURL     string   `json:"author_url"`
	Text          string   `json:"text"`
	ImageAssetIDs []string `json:"image_asset_ids,omitempty"`
}
