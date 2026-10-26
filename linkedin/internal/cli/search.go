package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const keywordSearchPageSize = 10

type keywordSearchResult struct {
	Query     string   `json:"query"`
	SearchURL string   `json:"search_url"`
	People    []person `json:"people"`
	Returned  int      `json:"returned"`
	HasMore   bool     `json:"has_more"`
}

type keywordSearchBatch struct {
	Searches  []keywordSearchResult `json:"searches"`
	FetchedAt string                `json:"fetched_at"`
}

type keywordSearchDocument struct {
	Query string `json:"query"`
	URL   string `json:"url"`
	HTML  string `json:"html"`
}

func runPeopleSearch(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("people search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _ = writeHelp(stderr, []string{"people", "search"}) }
	limit := fs.Int("limit", keywordSearchPageSize, "maximum people per query from the first page (1–10)")
	saveResponse := fs.String("save-response", "", "save raw search documents to a new private JSON file")
	queries, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		return writeHelp(stdout, []string{"people", "search"})
	}
	if err != nil {
		return err
	}
	if err := validateKeywordSearch(queries, *limit); err != nil {
		return err
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	var capture *os.File
	if *saveResponse != "" {
		capture, err = os.OpenFile(*saveResponse, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("create search capture %q: %w", *saveResponse, err)
		}
		defer capture.Close()
	}
	result, documents, err := client.searchKeywords(ctx, queries, *limit)
	if capture != nil {
		if saveErr := writeJSON(capture, documents); saveErr != nil {
			return fmt.Errorf("save search capture %q: %w (search error: %v)", *saveResponse, saveErr, err)
		}
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func validateKeywordSearch(queries []string, limit int) error {
	if len(queries) == 0 {
		return fmt.Errorf("people search requires at least one quoted keyword query; got 0 queries")
	}
	for i, query := range queries {
		if strings.TrimSpace(query) == "" {
			return fmt.Errorf("search query %d is blank: %q", i+1, query)
		}
	}
	if limit < 1 || limit > keywordSearchPageSize {
		return fmt.Errorf("search --limit must be between 1 and %d; got %d", keywordSearchPageSize, limit)
	}
	return nil
}

// The captured search navigation supplies the canonical /search/results/people/
// URL and keywords. Read its HTML document, as mutuals already does, rather than
// replaying the browser's RSC navigation, typeahead, history and analytics POSTs.
func (c *linkedinClient) searchKeywords(ctx context.Context, queries []string, limit int) (keywordSearchBatch, []keywordSearchDocument, error) {
	if err := validateKeywordSearch(queries, limit); err != nil {
		return keywordSearchBatch{}, nil, err
	}
	result := keywordSearchBatch{Searches: make([]keywordSearchResult, 0, len(queries))}
	documents := make([]keywordSearchDocument, 0, len(queries))
	for _, query := range queries {
		path := "/search/results/people/?" + url.Values{"keywords": {query}, "origin": {"GLOBAL_SEARCH_HEADER"}}.Encode()
		resp, err := c.get(ctx, path, http.Header{"Accept": {"text/html"}, "Accept-Language": {"en-US,en;q=0.9"}})
		if err != nil {
			return keywordSearchBatch{}, documents, fmt.Errorf("search query %q: %w", query, err)
		}
		const maxDocument = 8 << 20
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
		resp.Body.Close()
		documents = append(documents, keywordSearchDocument{Query: query, URL: "https://www.linkedin.com" + path, HTML: string(body)})
		if err != nil {
			return keywordSearchBatch{}, documents, fmt.Errorf("read search query %q: %w", query, err)
		}
		if len(body) > maxDocument {
			return keywordSearchBatch{}, documents, fmt.Errorf("search query %q exceeds %d bytes", query, maxDocument)
		}
		doc, screen, err := peopleSearchDocument(strings.NewReader(string(body)))
		if err != nil {
			return keywordSearchBatch{}, documents, fmt.Errorf("search query %q: %w", query, err)
		}
		// The requested URL alone cannot establish that the page applied keywords.
		if !searchQueryConfirmed(doc, query) {
			return keywordSearchBatch{}, documents, fmt.Errorf("search query %q is not confirmed by the page's search input; no generic results returned", query)
		}
		people, hasMore := []person{}, false
		if !searchHasExplicitEmptyState(screen) {
			people, hasMore, err = peopleSearchCards(screen)
			if err != nil {
				return keywordSearchBatch{}, documents, fmt.Errorf("search query %q: %w", query, err)
			}
		}
		if len(people) > limit {
			people, hasMore = people[:limit], true
		}
		result.Searches = append(result.Searches, keywordSearchResult{Query: query, SearchURL: "https://www.linkedin.com" + path, People: people, Returned: len(people), HasMore: hasMore})
	}
	result.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	return result, documents, nil
}

// Initial HTML leaves the input empty; its captured SetState action supplies
// the search box value during hydration. Decode those JSON records as data,
// never execute scripts or treat an echoed URL/initialParams as confirmation.
func searchQueryConfirmed(doc *html.Node, query string) bool {
	var flight strings.Builder
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if n.Data == "input" && attr(n, "componentkey") == "SearchResults_SearchTyahInputRef" && attr(n, "value") == query {
			return true
		}
		if n.Data != "script" || n.FirstChild == nil || n.FirstChild.Type != html.TextNode {
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
	for line := range strings.SplitSeq(flight.String(), "\n") {
		_, payload, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var value any
		if json.Unmarshal([]byte(payload), &value) == nil && hasSearchKeywordState(value, query) {
			return true
		}
	}
	return false
}

func searchHasExplicitEmptyState(screen *html.Node) bool {
	empty := false
	for n := range screen.Descendants() {
		if attr(n, "role") == "listitem" {
			return false
		}
		if n.Type != html.ElementNode || n.Data != "h2" || nodeText(n) != "No results found" {
			continue
		}
		visible := true
		for parent := n; parent != nil && parent != screen; parent = parent.Parent {
			if profileNodeHidden(parent) {
				visible = false
				break
			}
		}
		empty = empty || visible
	}
	return empty
}

func hasSearchKeywordState(value any, query string) bool {
	switch v := value.(type) {
	case map[string]any:
		if v["$type"] == "proto.sdui.actions.core.SetState" &&
			searchStateString(v, "value", "state", "key", "key", "value", "id") == "SearchResultsGlobalTyahKeywordsBinding" &&
			searchStateString(v, "value", "state", "value", "stringValue") == query {
			return true
		}
		for _, child := range v {
			if hasSearchKeywordState(child, query) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if hasSearchKeywordState(child, query) {
				return true
			}
		}
	}
	return false
}

func searchStateString(value any, path ...string) string {
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

func peopleSearchDocument(r io.Reader) (*html.Node, *html.Node, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, nil, fmt.Errorf("parse people search document: %w", err)
	}
	for n := range doc.Descendants() {
		if attr(n, "data-sdui-screen") == "com.linkedin.sdui.flagshipnav.search.SearchResultsPeople" {
			return doc, n, nil
		}
	}
	return nil, nil, fmt.Errorf("LinkedIn did not return the people search screen; check for a sign-in/checkpoint or changed response before retrying")
}
