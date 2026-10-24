package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/sync/errgroup"
)

type profileLink struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type profileBlock struct {
	Text  string        `json:"text"`
	Links []profileLink `json:"links,omitempty"`
}

type profileSection struct {
	Title     string         `json:"title"`
	SourceURL string         `json:"source_url"`
	Blocks    []profileBlock `json:"blocks"`
}

type fullProfileResult struct {
	profileResult
	FetchedAt string           `json:"fetched_at"`
	Sections  []profileSection `json:"sections"`
	Coverage  []string         `json:"coverage"`
}

type profileDocument struct {
	URL  string `json:"url"`
	HTML string `json:"html"`
}

type profileCapture struct {
	API       json.RawMessage   `json:"profile_api"`
	Documents []profileDocument `json:"documents"`
}

// The full experience URL is the profile's observed "Show all experiences"
// destination. It returns all rendered position groups in one document, avoiding
// a separate request for every role or a replay of all lazy UI components.
func (c *linkedinClient) fullProfile(ctx context.Context, ref profileRef) (fullProfileResult, profileCapture, error) {
	p, err := c.profile(ctx, ref)
	if err != nil {
		return fullProfileResult{}, profileCapture{}, err
	}
	if ref.ID != "" && p.ProfileURN != "urn:li:fsd_profile:"+ref.ID {
		return fullProfileResult{}, profileCapture{}, fmt.Errorf("profile %q resolved to a different identifier %q", ref.ID, p.ProfileURN)
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return fullProfileResult{}, profileCapture{}, fmt.Errorf("parse resolved profile URL %q: %w", p.URL, err)
	}
	experience := *u
	experience.Path = strings.TrimSuffix(u.Path, "/") + "/details/experience/"
	experience.RawQuery = url.Values{"isSelfProfile": {"false"}, "vieweeProfileId": {strings.TrimPrefix(p.ProfileURN, "urn:li:fsd_profile:")}}.Encode()
	capture := profileCapture{API: p.raw, Documents: []profileDocument{{URL: p.URL}, {URL: experience.String()}}}
	group, readCtx := errgroup.WithContext(ctx)
	group.SetLimit(2)
	for i := range capture.Documents {
		group.Go(func() error {
			// Only these two independent, read-only documents run concurrently.
			// Each has its own request clock; the shared http.Client is safe for
			// concurrent use. Other CLI commands keep their sequential pacing.
			reader := linkedinClient{http: c.http, session: c.session}
			body, err := reader.profileDocument(readCtx, capture.Documents[i].URL)
			capture.Documents[i].HTML = body
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return fullProfileResult{}, capture, err
	}
	result := fullProfileResult{profileResult: p, FetchedAt: time.Now().UTC().Format(time.RFC3339), Sections: []profileSection{}, Coverage: []string{
		"Profile overview and the full experience document; no contact-info overlay or messaging actions.",
		"Only sections present in the returned documents are included. Lazy sections and further pagination are not inferred to be empty.",
	}}
	for i, doc := range capture.Documents {
		sections, err := parseProfileDocument(doc, p.Name)
		if err != nil {
			return fullProfileResult{}, capture, err
		}
		if i == 1 {
			for j := range sections {
				if sections[j].Title == "Overview" {
					sections[j].Title = "Experience"
				}
			}
		}
		result.Sections = append(result.Sections, sections...)
	}
	return result, capture, nil
}

func (c *linkedinClient) profileDocument(ctx context.Context, source string) (string, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Host != "www.linkedin.com" || u.User != nil || !strings.HasPrefix(u.Path, "/in/") {
		return "", fmt.Errorf("invalid profile document URL %q", source)
	}
	resp, err := c.get(ctx, u.RequestURI(), http.Header{"Accept": {"text/html"}, "Accept-Language": {"en-US,en;q=0.9"}})
	if err != nil {
		return "", fmt.Errorf("fetch profile document %q: %w", source, err)
	}
	defer resp.Body.Close()
	const maxDocument = 16 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return "", fmt.Errorf("read profile document %q: %w", source, err)
	}
	if len(body) > maxDocument {
		return "", fmt.Errorf("profile document %q exceeds %d bytes", source, maxDocument)
	}
	return string(body), nil
}

func parseProfileDocument(doc profileDocument, name string) ([]profileSection, error) {
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return nil, fmt.Errorf("parse profile document %q: %w", doc.URL, err)
	}
	var primary *html.Node
	var title string
	for n := range root.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if n.Data == "title" {
			title = profileText(n)
		}
		if primary == nil && (attr(n, "aria-label") == "Primary content" || n.Data == "main" || attr(n, "role") == "main") {
			primary = n
		}
	}
	if primary == nil || !strings.HasPrefix(title, name+" | LinkedIn") {
		return nil, fmt.Errorf("profile document %q does not confirm %q and a primary profile content region; check authentication or a changed response", doc.URL, name)
	}
	sections := []profileSection{{Title: "Overview", SourceURL: doc.URL, Blocks: []profileBlock{}}}
	base, _ := url.Parse(doc.URL)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if profileNodeHidden(n) {
			return
		}
		if n.Type == html.ElementNode {
			var text string
			switch n.Data {
			case "h1", "h2", "h3", "h4", "p", "a":
				text = profileText(n)
			}
			if n.Data == "h1" && text == name {
				return
			}
			if n.Data == "h2" {
				if text != "" {
					sections = append(sections, profileSection{Title: text, SourceURL: doc.URL, Blocks: []profileBlock{}})
				}
				return
			}
			if n.Data == "p" || n.Data == "a" || n.Data == "h3" || n.Data == "h4" {
				if !profileTextUseful(text) {
					return
				}
				block := profileBlock{Text: text}
				linkNodes := []*html.Node{n}
				for a := range n.Descendants() {
					if a.Type == html.ElementNode && a.Data == "a" && !profileNodeHidden(a) {
						linkNodes = append(linkNodes, a)
					}
				}
				for _, a := range linkNodes {
					if a.Data != "a" {
						continue
					}
					if href := attr(a, "href"); href != "" {
						u, err := base.Parse(href)
						if err == nil && u.Hostname() == "www.linkedin.com" && u.Path == "/safety/go/" {
							if target, targetErr := url.Parse(u.Query().Get("url")); targetErr == nil && target.Host != "" {
								u = target
							}
						}
						if err == nil && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") {
							block.Links = append(block.Links, profileLink{Text: profileText(a), URL: u.String()})
						}
					}
				}
				appendProfileBlock(&sections[len(sections)-1], block)
				return
			}
		}
		if n.Type == html.TextNode {
			if text := strings.Join(strings.Fields(n.Data), " "); profileTextUseful(text) {
				appendProfileBlock(&sections[len(sections)-1], profileBlock{Text: text})
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(primary)
	nonempty := sections[:0]
	for _, s := range sections {
		if len(s.Blocks) > 0 {
			nonempty = append(nonempty, s)
		}
	}
	if len(nonempty) == 0 {
		return nil, fmt.Errorf("profile document %q returned no readable primary profile content", doc.URL)
	}
	return nonempty, nil
}

func profileNodeHidden(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "script", "style", "svg", "img", "button", "nav", "header", "footer", "aside", "dialog":
		return true
	}
	if attr(n, "aria-hidden") == "true" || attr(n, "role") == "dialog" || attr(n, "role") == "complementary" || attr(n, "aria-label") == "Aside" || attr(n, "aria-label") == "Footer" {
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" {
			return true
		}
	}
	if n.Data == "a" {
		href := attr(n, "href")
		if href == "/" || href == "https://www.linkedin.com/" {
			return true
		}
		for _, route := range []string{"/messaging/", "/preload/", "/overlay/contact-info/", "/premium/"} {
			if strings.Contains(href, route) {
				return true
			}
		}
	}
	return false
}

func appendProfileBlock(section *profileSection, block profileBlock) {
	if len(section.Blocks) > 0 {
		last := section.Blocks[len(section.Blocks)-1]
		if last.Text == block.Text && len(last.Links) == len(block.Links) {
			equal := true
			for i := range last.Links {
				if last.Links[i] != block.Links[i] {
					equal = false
					break
				}
			}
			if equal {
				return
			}
		}
	}
	section.Blocks = append(section.Blocks, block)
}

func profileTextUseful(text string) bool {
	// The initial document contains both first- and second/third-degree UI
	// alternatives, with visibility decided later by client state. Neither is
	// evidence of the relationship. Mutuals have their own verified command.
	switch text {
	case "", "·", "· 1st", "· 2nd", "· 3rd", "· 3rd+":
		return false
	}
	return true
}

func profileText(n *html.Node) string {
	var pieces []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if profileNodeHidden(n) {
			return
		}
		if n.Type == html.TextNode {
			pieces = append(pieces, n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(strings.Join(pieces, " ")), " ")
}
