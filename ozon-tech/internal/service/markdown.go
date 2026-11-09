package service

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/meoyawn/clis/ozon-tech/internal/domain"
	"golang.org/x/net/html"
)

var htmlWhitespace = regexp.MustCompile(`[\t\n\r ]+`)
var blankLines = regexp.MustCompile(`\n[\t ]*\n(?:[\t ]*\n)+`)
var trailingSpaces = regexp.MustCompile(`[\t ]+\n`)
var markdownEscapes = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "`", "\\`")

func descriptionMarkdown(description *domain.Description) (string, error) {
	for _, raw := range [][]byte{description.Quote, description.Blocks} {
		value := strings.TrimSpace(string(raw))
		if value != "" && value != "null" && value != "[]" {
			return "", fmt.Errorf("unsupported structured description; preserve quote/blocks support before updating the catalog")
		}
	}
	doc, err := html.Parse(strings.NewReader(description.HTML))
	if err != nil {
		return "", fmt.Errorf("parse description HTML: %w", err)
	}
	markdown := strings.TrimSpace(blankLines.ReplaceAllString(trailingSpaces.ReplaceAllString(renderNode(doc), "\n"), "\n\n"))
	if markdown == "" {
		return "", fmt.Errorf("empty vacancy description")
	}
	return markdown + "\n", nil
}

func renderNode(node *html.Node) string {
	if node.Type == html.TextNode {
		return markdownEscapes.Replace(htmlWhitespace.ReplaceAllString(node.Data, " "))
	}
	if node.Type == html.ElementNode && (node.Data == "script" || node.Data == "style") {
		return ""
	}
	var content strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		content.WriteString(renderNode(child))
	}
	value := content.String()
	if node.Type != html.ElementNode {
		return value
	}
	switch node.Data {
	case "p", "div", "section", "ul", "ol":
		return "\n\n" + strings.TrimSpace(value) + "\n\n"
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "\n\n" + strings.Repeat("#", int(node.Data[1]-'0')) + " " + strings.TrimSpace(value) + "\n\n"
	case "li":
		prefix := "- "
		if node.Parent != nil && node.Parent.Data == "ol" {
			prefix = "1. "
		}
		return "\n" + prefix + strings.ReplaceAll(strings.TrimSpace(value), "\n", "\n   ") + "\n"
	case "strong", "b":
		return "**" + strings.TrimSpace(value) + "**"
	case "em", "i":
		return "*" + strings.TrimSpace(value) + "*"
	case "br":
		return "\n"
	case "a":
		for _, attr := range node.Attr {
			if attr.Key == "href" {
				base, err := url.Parse(Origin)
				if err != nil {
					panic(err)
				}
				target, err := base.Parse(attr.Val)
				if err == nil && (target.Scheme == "https" || target.Scheme == "http" || target.Scheme == "mailto") {
					return "[" + strings.TrimSpace(value) + "](<" + target.String() + ">)"
				}
			}
		}
	case "blockquote":
		return "\n\n> " + strings.ReplaceAll(strings.TrimSpace(value), "\n", "\n> ") + "\n\n"
	}
	return value
}
