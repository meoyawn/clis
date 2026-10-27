package cli

import (
	"encoding/json"
	"strconv"
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
	return readFlightData(flight.String())
}

func readFlightData(flight string) documentData {
	rows := documentData{}
	for flight != "" {
		id, rest, ok := strings.Cut(flight, ":")
		if !ok {
			break
		}
		// React length-prefixed text/binary records need not end in a newline.
		// An empty Uint8Array (id:o0,) can precede the model-state JSON row.
		if len(rest) > 0 && strings.ContainsRune("TAOoUSsLlGgMmV", rune(rest[0])) {
			if length, data, found := strings.Cut(rest[1:], ","); found {
				if size, err := strconv.ParseUint(length, 16, 32); err == nil && size <= uint64(len(data)) {
					if rest[0] == 'T' {
						rows[id] = data[:size]
					} else if rest[0] == 'o' {
						// Uint8Array's native JSON representation uses numeric keys.
						value := map[string]any{}
						for i, b := range []byte(data[:size]) {
							value[strconv.Itoa(i)] = int(b)
						}
						rows[id] = value
					}
					flight = data[size:]
					continue
				}
			}
		}
		payload, next, _ := strings.Cut(rest, "\n")
		flight = next
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
