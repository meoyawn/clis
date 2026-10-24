package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

type Object = map[string]any

// DecodeState supports both HH's XHR JSON and its server-rendered template.
// It parses data rather than executing the site's scripts.
func DecodeState(body []byte) (Object, error) {
	var state Object
	if json.Unmarshal(body, &state) == nil && state != nil {
		return state, nil
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse HH page: %w", err)
	}
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "template" && attr(n, "id") == "HH-Lux-InitialState" {
			found = textContent(n)
			return
		}
		for child := n.FirstChild; child != nil && found == ""; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if found == "" {
		return nil, fmt.Errorf("HH page has no HH-Lux-InitialState: authentication, checkpoint, or changed page format; no write attempted")
	}
	if err := json.Unmarshal([]byte(found), &state); err != nil {
		return nil, fmt.Errorf("decode HH-Lux-InitialState: %w", err)
	}
	return state, nil
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var out strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		out.WriteString(textContent(child))
	}
	return out.String()
}

// PrintText extracts the actual print page, excluding executable/template data.
// Links retain their destinations so an agent can audit every application link.
func PrintText(body []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("parse print view: %w", err)
	}
	var root *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode && attr(n, "id") == "HH-React-Root" {
			root = n
			return
		}
		for c := n.FirstChild; c != nil && root == nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if root == nil {
		return "", fmt.Errorf("print view has no HH-React-Root: authentication or page contract changed")
	}
	var out strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
			return
		}
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "script", "style", "template", "svg", "button", "noscript":
			return
		}
		if attr(n, "aria-hidden") == "true" || attr(n, "hidden") != "" {
			return
		}
		block := strings.Contains("|div|p|h1|h2|h3|h4|li|section|tr|br|", "|"+n.Data+"|")
		if block {
			out.WriteByte('\n')
		}
		if n.Data == "a" {
			out.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
		if n.Data == "a" {
			href := attr(n, "href")
			if href != "" && !strings.Contains(textContent(n), href) && !strings.HasPrefix(href, "tel:") && !strings.HasPrefix(href, "mailto:") {
				out.WriteString(" (" + href + ")")
			}
			out.WriteByte(' ')
		}
		if block {
			out.WriteByte('\n')
		}
	}
	visit(root)
	var lines []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// ReadPrintContacts inventories rendered anchors, including extra contacts that
// are absent from an expected patch or stale embedded state. Generated phone
// shortcuts are reported separately: their labels do not prove an account exists.
func ReadPrintContacts(body []byte) (PrintContacts, error) {
	out := PrintContacts{Email: []string{}, Phone: []string{}, Websites: []ContactLink{}, Messengers: []ContactLink{}, GeneratedPhoneShortcuts: []string{}}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return out, fmt.Errorf("parse print contacts: %w", err)
	}
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inRoot bool) {
		inRoot = inRoot || attr(n, "id") == "HH-React-Root"
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "template", "noscript", "svg":
				return
			}
			if attr(n, "aria-hidden") == "true" {
				return
			}
		}
		if inRoot && n.Type == html.ElementNode && n.Data == "a" {
			href, qa := attr(n, "href"), attr(n, "data-qa")
			label := strings.Join(strings.Fields(textContent(n)), " ")
			switch {
			case strings.HasPrefix(href, "mailto:"):
				out.Email = append(out.Email, strings.TrimPrefix(href, "mailto:"))
			case strings.HasPrefix(href, "tel:"):
				out.Phone = append(out.Phone, strings.TrimPrefix(href, "tel:"))
			case strings.HasPrefix(qa, "resume-phone-deep-link-"):
				out.GeneratedPhoneShortcuts = append(out.GeneratedPhoneShortcuts, label)
			case strings.HasPrefix(qa, "resume-communication-method-"):
				out.Messengers = append(out.Messengers, ContactLink{Value: href, Description: label})
			case strings.HasPrefix(qa, "resume-personalsite-") || strings.HasPrefix(qa, "resume-contact-social-site-"):
				out.Websites = append(out.Websites, ContactLink{Value: href, Description: label})
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inRoot)
		}
	}
	walk(doc, false)
	return out, nil
}

func EncodeJSON(w io.Writer, value any) error {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	return e.Encode(value)
}
