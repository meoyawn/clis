package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"golang.org/x/net/html"
)

// ParsePostDocument reads the exact permalink's visible post, excluding comments,
// sidebar content, scripts and unrelated cards. Markers were observed in Chrome.
func ParsePostDocument(ref domain.PostRef, document string) (domain.Post, error) {
	root, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return domain.Post{}, fmt.Errorf("parse post document: %w", err)
	}
	var primary *html.Node
	for n := range root.Descendants() {
		if n.Type == html.ElementNode && postAttr(n, "aria-label") == "Primary content" && !postHidden(n) {
			if primary != nil {
				return domain.Post{}, fmt.Errorf("post %q has multiple primary regions", ref.URL())
			}
			primary = n
		}
	}
	var card *html.Node
	var exactActivity bool
	if primary != nil {
		for n := range primary.Descendants() {
			if n.Type != html.ElementNode || postHidden(n) {
				continue
			}
			if activity, ok := strings.CutPrefix(postAttr(n, "data-testid"), "ReactionFacepileCollection-"); ok {
				if activity != ref.URN() {
					return domain.Post{}, fmt.Errorf("post %q primary region contains a different activity", ref.URL())
				}
				exactActivity = true
			}
			if postAttr(n, "role") == "listitem" && strings.HasPrefix(postAttr(n, "componentkey"), "update-card-focus") {
				if card != nil {
					return domain.Post{}, fmt.Errorf("post %q has multiple feed cards", ref.URL())
				}
				card = n
			}
		}
	}
	if card == nil || !exactActivity {
		return domain.Post{}, fmt.Errorf("post %q has no exact activity-bound card; check authentication or a changed page", ref.URL())
	}
	result := domain.Post{URL: ref.URL(), URN: ref.URN()}
	var textNode *html.Node
	var heading bool
	for n := range card.Descendants() {
		if n.Type != html.ElementNode || postHidden(n) {
			continue
		}
		if n.Data == "h2" && strings.TrimSpace(postText(n)) == "Feed post" {
			heading = true
		}
		if n.Data == "img" {
			for _, attr := range []string{"src", "data-delayed-url"} {
				u, err := url.Parse(postAttr(n, attr))
				if err != nil || u.Scheme != "https" || u.Host != "media.licdn.com" {
					continue
				}
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) > 3 && parts[0] == "dms" && parts[1] == "image" && parts[2] == "v2" && assetIDPattern.MatchString(parts[3]) {
					result.ImageAssetIDs = append(result.ImageAssetIDs, parts[3])
				}
			}
		}
		if postAttr(n, "data-testid") == "expandable-text-box" {
			if textNode != nil {
				return domain.Post{}, fmt.Errorf("post %q has multiple commentary blocks", ref.URL())
			}
			textNode = n
		}
		if result.AuthorURL == "" && textNode == nil && n.Data == "a" && strings.TrimSpace(postText(n)) != "" {
			u, err := url.Parse(postAttr(n, "href"))
			if err == nil && u.Scheme == "https" && u.Host == "www.linkedin.com" && u.User == nil {
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) == 2 && parts[0] == "in" && parts[1] != "" {
					result.AuthorURL = "https://www.linkedin.com/in/" + parts[1] + "/"
				}
			}
		}
	}
	if !heading || textNode == nil || result.AuthorURL == "" {
		return domain.Post{}, fmt.Errorf("post %q does not expose an unambiguous author and full commentary", ref.URL())
	}
	result.Text = strings.TrimSpace(postText(textNode))
	if result.Text == "" {
		return domain.Post{}, fmt.Errorf("post %q has empty commentary", ref.URL())
	}
	return result, nil
}

func postAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func postHidden(n *html.Node) bool {
	for p := n; p != nil; p = p.Parent {
		if p.Type == html.ElementNode {
			if p.Data == "script" || p.Data == "style" || p.Data == "template" || postAttr(p, "aria-hidden") == "true" {
				return true
			}
			for _, a := range p.Attr {
				if a.Key == "hidden" || a.Key == "inert" {
					return true
				}
			}
		}
	}
	return false
}

func postText(n *html.Node) string {
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if postHidden(n) {
			return
		}
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && n.Data == "br" {
			text.WriteByte('\n')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(n)
	return text.String()
}
