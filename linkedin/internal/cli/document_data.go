package cli

import (
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
)

// Decode hydration records as data. No script or action is executed.
type documentData map[string]any

func readDocumentData(doc *html.Node) documentData {
	var flight strings.Builder
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "script" || n.FirstChild == nil || n.FirstChild.Type != html.TextNode {
			continue
		}
		text, ok := strings.CutPrefix(strings.TrimSpace(n.FirstChild.Data), "window.__como_rehydration__ =")
		if !ok {
			continue
		}
		var chunks []string
		if json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(text), ";")), &chunks) != nil {
			continue
		}
		for _, chunk := range chunks {
			flight.WriteString(chunk)
		}
	}
	rows := documentData{}
	for line := range strings.SplitSeq(flight.String(), "\n") {
		id, payload, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(payload), &value) == nil {
			if _, duplicate := rows[id]; duplicate {
				return nil
			}
			rows[id] = value
		}
	}
	return rows
}

func (d documentData) resolve(value any) any {
	ref, ok := value.(string)
	if !ok || len(ref) < 3 || (ref[:2] != "$L" && ref[:2] != "$Q") {
		return value
	}
	if resolved, ok := d[ref[2:]]; ok {
		return resolved
	}
	return value
}

func (d documentData) walk(value any, visit func(map[string]any)) {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			visit(v)
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		case string:
			if seen[v] {
				return
			}
			seen[v] = true
			if resolved := d.resolve(v); resolved != v {
				walk(resolved)
			}
		}
	}
	walk(value)
}

func jsonStringAt(value any, path ...string) string {
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = object[key]
	}
	text, _ := value.(string)
	return text
}
