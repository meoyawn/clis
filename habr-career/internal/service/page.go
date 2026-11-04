package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

type Object = map[string]any

type page struct {
	state Object
	csrf  string
	doc   *html.Node
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func find(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := find(child, match); found != nil {
			return found
		}
	}
	return nil
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "svg") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (n.Data == "p" || n.Data == "div" || n.Data == "li" || n.Data == "br" || n.Data == "section" || strings.HasPrefix(n.Data, "h")) {
			b.WriteByte('\n')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			href := attr(n, "href")
			if strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:") {
				b.WriteString(" (" + href + ")")
			}
		}
		if n.Type == html.ElementNode {
			b.WriteByte(' ')
		}
	}
	walk(n)
	var lines []string
	for _, line := range strings.Split(b.String(), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func htmlText(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return ""
	}
	return nodeText(doc)
}

func decodePage(body []byte) (*page, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse Habr Career page: %w", err)
	}
	stateNode := find(doc, func(n *html.Node) bool { return n.Data == "script" && attr(n, "data-ssr-state") == "true" })
	if stateNode == nil || stateNode.FirstChild == nil {
		return nil, fmt.Errorf("page has no native SSR state; authentication, protection or request contract changed; stop without retrying")
	}
	var state Object
	if err := json.Unmarshal([]byte(stateNode.FirstChild.Data), &state); err != nil {
		return nil, fmt.Errorf("decode native SSR state: %w", err)
	}
	csrf := ""
	if meta := find(doc, func(n *html.Node) bool { return n.Data == "meta" && attr(n, "name") == "csrf-token" }); meta != nil {
		csrf = attr(meta, "content")
	}
	return &page{state: state, csrf: csrf, doc: doc}, nil
}

func object(value any) Object {
	out, _ := value.(map[string]any)
	return out
}

func number(value any) string {
	if n, ok := value.(float64); ok && n > 0 && n == float64(int64(n)) {
		return fmt.Sprintf("%.0f", n)
	}
	return ""
}

func redact(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := Object{}
		for key, value := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "cookie") || strings.Contains(lower, "password") || strings.Contains(lower, "csrf") || strings.Contains(lower, "authorization") || strings.Contains(lower, "secret") {
				continue
			}
			out[key] = redact(value)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redact(item)
		}
		return out
	default:
		return value
	}
}
