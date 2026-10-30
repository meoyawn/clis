package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

type object = map[string]any

func obj(x any) object {
	if v, ok := x.(map[string]any); ok {
		return v
	}
	return nil
}
func arr(x any) []any {
	if v, ok := x.([]any); ok {
		return v
	}
	return nil
}
func at(x any, keys ...string) any {
	for _, k := range keys {
		x = obj(x)[k]
	}
	return x
}
func str(x any) string {
	switch v := x.(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}
func boolean(x any) *bool {
	v, ok := x.(bool)
	if !ok {
		return nil
	}
	return &v
}
func boolValue(x any) bool {
	v := boolean(x)
	return v != nil && *v
}
func number(x any) *float64 {
	v, err := strconv.ParseFloat(str(x), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
func integer(x any) *int64 {
	v, err := strconv.ParseInt(str(x), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}
func intValue(x any) int64 {
	v := integer(x)
	if v != nil {
		return *v
	}
	return 0
}

func (p *page) widgets(prefix string) ([]object, error) {
	keys := make([]string, 0)
	for k := range p.WidgetStates {
		if strings.HasPrefix(k, prefix+"-") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	result := make([]object, 0, len(keys))
	for _, k := range keys {
		v, err := p.widget(k)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, nil
}

func (p *page) widget(id string) (object, error) {
	raw := bytes.TrimSpace(p.WidgetStates[id])
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, fmt.Errorf("invalid %s widget state", id)
		}
		raw = []byte(s)
	}
	var v object
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil || v == nil {
		return nil, fmt.Errorf("invalid %s widget schema", id)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("invalid trailing data in %s widget state", id)
	}
	return v, nil
}
func (p *page) first(prefix string) (object, error) {
	w, e := p.widgets(prefix)
	if e != nil || len(w) == 0 {
		return nil, e
	}
	return w[0], nil
}

var numericText = regexp.MustCompile(`[0-9]+(?:[.,][0-9]+)?`)

func compactNumber(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}
func textNumber(s string) *float64 {
	return number(strings.ReplaceAll(numericText.FindString(compactNumber(s)), ",", "."))
}
func textInteger(s string) *int64 {
	match := numericText.FindString(compactNumber(s))
	if strings.ContainsAny(match, ".,") {
		return nil
	}
	return integer(match)
}
func money(x any) *Money {
	s := str(x)
	if s == "" {
		s = text(x)
	}
	n := textNumber(s)
	if n == nil || *n < 0 {
		return nil
	}
	return &Money{Amount: *n, Text: s}
}

// text reads presentation text only, never actions, telemetry or personal page state.
func text(x any) string {
	switch v := x.(type) {
	case string:
		return plainText(v)
	case []any:
		var parts []string
		for _, item := range v {
			if at(item, "type") == "newLine" {
				parts = append(parts, "\n")
			} else {
				parts = append(parts, text(item))
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	case map[string]any:
		for _, k := range []string{"text", "content", "textRs", "title", "headRs", "textAtom"} {
			if value, ok := v[k]; ok {
				return text(value)
			}
		}
	}
	return ""
}
func plainText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return strings.TrimSpace(s)
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := false
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.TrimSpace(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			t := z.Token()
			if t.Data == "script" || t.Data == "style" {
				skip = true
			}
			if t.Data == "br" || t.Data == "p" || t.Data == "li" {
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			t := z.Token()
			if t.Data == "script" || t.Data == "style" {
				skip = false
			}
			if t.Data == "p" || t.Data == "li" || t.Data == "div" {
				b.WriteByte('\n')
			}
		case html.TextToken:
			if !skip {
				b.Write(z.Text())
			}
		}
	}
}
func publicURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	if u.IsAbs() {
		if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return ""
		}
	} else {
		if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
			return ""
		}
		u.Scheme = "https"
		u.Host = "www.ozon.ru"
	}
	if u.Host == "ozon.ru" || u.Host == "www.ozon.ru" {
		u.Scheme = "https"
		u.Host = "www.ozon.ru"
		u.RawQuery = ""
		u.Fragment = ""
	}
	return u.String()
}
func addUnique(list []string, v string) []string {
	if v != "" && !slices.Contains(list, v) {
		return append(list, v)
	}
	return list
}
func mergeMeta(dst *Metadata, src Metadata) {
	dst.HTTPRequests += src.HTTPRequests
	dst.SourceURLs = append(dst.SourceURLs, src.SourceURLs...)
	dst.FetchedAt = src.FetchedAt
	if src.RetryAfter != "" {
		dst.RetryAfter = src.RetryAfter
	}
}

// ReadPageHTML is used by offline HAR tests; live commands request the same
// widget states as JSON and never execute HTML scripts.
func readPageHTML(r io.Reader) (*page, error) {
	p := &page{WidgetStates: map[string]json.RawMessage{}}
	z := html.NewTokenizer(r)
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if !errors.Is(z.Err(), io.EOF) {
				return nil, z.Err()
			}
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		t := z.Token()
		var id, state string
		for _, a := range t.Attr {
			if a.Key == "id" {
				id = a.Val
			}
			if a.Key == "data-state" {
				state = a.Val
			}
		}
		if strings.HasPrefix(id, "state-") && state != "" {
			p.WidgetStates[strings.TrimPrefix(id, "state-")] = json.RawMessage(state)
		}
	}
	return p, nil
}
