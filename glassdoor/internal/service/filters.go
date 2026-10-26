package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/meoyawn/clis/glassdoor/internal/domain"
	"golang.org/x/net/html"
)

const FilterSourceURL = Origin + "/Salaries/software-engineer-salary-SRCH_KO0,17.htm"

func (c *Client) Industries(ctx context.Context) ([]domain.Industry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, FilterSourceURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Accept", "text/html")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read native industry catalog: %w; not retried", err)
	}
	defer res.Body.Close()
	data, err := readResponse(res)
	if err != nil {
		return nil, fmt.Errorf("industry catalog: %w", err)
	}
	return parseIndustries(bytes.NewReader(data))
}

// Decode JSON payloads from the observed Next Flight transport; never execute
// captured page scripts, telemetry or other JavaScript.
func parseIndustries(reader io.Reader) ([]domain.Industry, error) {
	doc, err := html.Parse(reader)
	if err != nil {
		return nil, err
	}
	var stream strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			var content strings.Builder
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.TextNode {
					content.WriteString(child.Data)
				}
			}
			text := content.String()
			const prefix = "self.__next_f.push("
			for {
				idx := strings.Index(text, prefix)
				if idx < 0 {
					break
				}
				text = text[idx+len(prefix):]
				dec := json.NewDecoder(strings.NewReader(text))
				var parts []json.RawMessage
				if dec.Decode(&parts) != nil {
					break
				}
				text = text[dec.InputOffset():]
				if len(parts) == 2 && string(parts[0]) == "1" {
					var chunk string
					if json.Unmarshal(parts[1], &chunk) == nil {
						stream.WriteString(chunk)
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	var found []domain.Industry
	var inspect func(any) error
	inspect = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			if list, ok := v["industries"].([]any); ok && len(list) > 0 {
				data, _ := json.Marshal(list)
				var next []domain.Industry
				if err := json.Unmarshal(data, &next); err != nil {
					return err
				}
				for _, entry := range next {
					if entry.ID <= 0 || entry.Name == "" {
						return fmt.Errorf("invalid native industry catalog entry")
					}
				}
				if len(found) > 0 {
					old, _ := json.Marshal(found)
					if string(old) != string(data) {
						return fmt.Errorf("conflicting native industry catalogs")
					}
				} else {
					found = next
				}
			}
			for _, x := range v {
				if err := inspect(x); err != nil {
					return err
				}
			}
		case []any:
			for _, x := range v {
				if err := inspect(x); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for line := range strings.SplitSeq(stream.String(), "\n") {
		_, text, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var record any
		if json.Unmarshal([]byte(text), &record) == nil {
			if err = inspect(record); err != nil {
				return nil, err
			}
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("native salary page has no readable industry catalog; access challenge or changed page format, stopped")
	}
	return found, nil
}
