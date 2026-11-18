package cli

import (
	"context"
	"fmt"
	"golang.org/x/net/html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var allPositionMedia = regexp.MustCompile(`^Show all ([1-9][0-9]*) media$`)

func (c *linkedinClient) allPositionMediaLinks(ctx context.Context, p fullProfileResult, id string) ([]profileLink, error) {
	visible, found := positionMediaLinks(p, id)
	if !found {
		return nil, fmt.Errorf("position %s is absent from the rendered profile", id)
	}
	for _, link := range visible {
		m := allPositionMedia.FindStringSubmatch(link.Text)
		if m == nil {
			continue
		}
		target, err := url.Parse(link.URL)
		if err != nil {
			return nil, err
		}
		if "https://www.linkedin.com"+target.Path != p.URL+"overlay/Position/"+id+"/image-list/" || target.Query().Get("profileId") != strings.TrimPrefix(p.ProfileURN, "urn:li:fsd_profile:") {
			return nil, fmt.Errorf("unexpected position media-list destination")
		}
		document, err := c.profileDocument(ctx, link.URL)
		if err != nil {
			return nil, err
		}
		doc, err := html.Parse(strings.NewReader(document))
		if err != nil {
			return nil, err
		}
		count, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, err
		}
		return parsePositionMediaList(readDocumentData(doc), count)
	}
	return visible, nil
}

func parsePositionMediaList(data documentData, count int) ([]profileLink, error) {
	links := []profileLink{}
	seen := map[profileLink]bool{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["direction"] != "horizontal" {
				return
			}
			children, _ := m["children"].([]any)
			if len(children) != 2 {
				return
			}
			var href string
			data.walk(children[0], func(node map[string]any) {
				value := jsonStringAt(node, "urlValue", "url")
				u, err := url.Parse(value)
				if err != nil || u.Host != "www.linkedin.com" || u.Path != "/safety/go/" {
					return
				}
				dest, err := url.Parse(u.Query().Get("url"))
				if err == nil && dest.Host != "" && dest.User == nil && (dest.Scheme == "http" || dest.Scheme == "https") {
					href = dest.String()
				}
			})
			if href == "" {
				return
			}
			label := firstMediaParagraph(data, children[1], 0)
			if label == "" {
				return
			}
			link := profileLink{Text: label, URL: href}
			if !seen[link] {
				seen[link] = true
				links = append(links, link)
			}
		})
	}
	if len(links) != count {
		return nil, fmt.Errorf("position media list exposes %d distinct cards, expected %d", len(links), count)
	}
	return links, nil
}

func firstMediaParagraph(data documentData, value any, depth int) string {
	if depth > 30 {
		return ""
	}
	switch value := value.(type) {
	case string:
		if resolved := data.resolve(value); resolved != value {
			return firstMediaParagraph(data, resolved, depth+1)
		}
	case []any:
		if len(value) == 4 && value[1] == "p" {
			props, _ := value[3].(map[string]any)
			children, _ := props["children"].([]any)
			if len(children) == 1 {
				text, _ := children[0].(string)
				return text
			}
		}
		for _, child := range value {
			if label := firstMediaParagraph(data, child, depth+1); label != "" {
				return label
			}
		}
	case map[string]any:
		return firstMediaParagraph(data, value["children"], depth+1)
	}
	return ""
}
