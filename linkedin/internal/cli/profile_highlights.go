package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

const profileAboveActivity = "com.linkedin.sdui.generated.profile.dsl.impl.profileCardsAboveActivity"

type profileComponentCapture struct {
	URL      string          `json:"url"`
	Request  json.RawMessage `json:"request"`
	Response string          `json:"response"`
}

// Only read the deferred card request exposed by this exact profile. The
// component POST renders profile content; it does not execute card actions.
func (c *linkedinClient) profileHighlights(ctx context.Context, doc profileDocument, target profileResult) (*profileSection, *profileComponentCapture, error) {
	root, err := html.Parse(strings.NewReader(doc.HTML))
	if err != nil {
		return nil, nil, fmt.Errorf("parse profile highlights request: %w", err)
	}
	ref, err := parseProfile(target.URL)
	if err != nil {
		return nil, nil, err
	}
	id := strings.TrimPrefix(target.ProfileURN, "urn:li:fsd_profile:")
	data := readDocumentData(root)
	var arguments map[string]any
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.AsyncComponentRequest" || m["newComponentId"] != profileAboveActivity {
				return
			}
			args, _ := m["requestedArguments"].(map[string]any)
			payload, _ := args["payload"].(map[string]any)
			states, ok := args["requestedStateKeys"].([]any)
			if !ok || len(states) != 0 || payload["vanityName"] != ref.Slug || payload["isSelfView"] != false || jsonStringAt(payload, "replaceableSectionArgs", "vieweeProfileId") != id {
				return
			}
			arguments = args
		})
	}
	if arguments == nil {
		return nil, nil, nil
	}
	clientArgs := map[string]any{"states": []any{}, "screenId": "com.linkedin.sdui.flagshipnav.profile.Profile", "knownTemplateIds": []any{}}
	for _, key := range []string{"payload", "requestMetadata"} {
		clientArgs[key] = arguments[key]
	}
	body, err := json.Marshal(map[string]any{"componentId": profileAboveActivity, "clientArguments": clientArgs})
	if err != nil {
		return nil, nil, err
	}
	path := "/flagship-web/rsc-action/actions/component?componentId=" + profileAboveActivity + "&sduiid=" + profileAboveActivity
	capture := &profileComponentCapture{URL: "https://www.linkedin.com" + path, Request: body}
	response, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(body), http.Header{
		"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}, "X-Li-Rsc-Stream": {"true"},
	})
	if err != nil {
		return nil, capture, fmt.Errorf("read profile highlights: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	capture.Response = string(raw)
	if err != nil {
		return nil, capture, fmt.Errorf("read profile highlights response: %w", err)
	}
	if len(raw) > 16<<20 {
		return nil, capture, fmt.Errorf("profile highlights response exceeds 16 MiB")
	}
	section, err := parseProfileHighlights(string(raw), target)
	return section, capture, err
}

func parseProfileHighlights(body string, target profileResult) (*profileSection, error) {
	data := readFlightData(body)
	key := "com.linkedin.sdui.profile.card.ref" + strings.TrimPrefix(target.ProfileURN, "urn:li:fsd_profile:") + "SalesInsightsOrHighlights"
	found := false
	var content any
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["componentKey"] == key {
				found = true
				if initial := m["initialContent"]; initial != nil {
					content = initial
				}
			}
		})
	}
	if !found {
		return nil, fmt.Errorf("profile highlights response does not confirm target %q; no shared-history claims returned", target.URL)
	}
	section := &profileSection{SourceURL: target.URL, Blocks: []profileBlock{}}
	walkProfileComponent(data, content, func(props map[string]any) bool {
		if textProps, ok := props["textProps"].(map[string]any); ok {
			props = textProps
		}
		tag, _ := props["tagName"].(string)
		if tag == "h2" {
			section.Title = profileComponentText(data, props["children"])
			return false
		}
		if tag == "p" || tag == "h3" || tag == "h4" {
			if text := profileComponentText(data, props["children"]); text != "" {
				appendProfileBlock(section, profileBlock{Text: text})
			}
			return false
		}
		button, ok := props["buttonProps"].(map[string]any)
		if !ok || button["disabled"] == true || button["aria-disabled"] == "true" {
			return true
		}
		label, _ := button["aria-label"].(string)
		if label == "" {
			label = profileComponentText(data, button["text"])
		}
		data.walk(props["triggers"], func(action map[string]any) {
			if action["$type"] != "proto.sdui.actions.core.NavigateToUrl" || label == "" {
				return
			}
			u, err := url.Parse(jsonStringAt(action, "urlValue", "url"))
			if err != nil || u.User != nil || (u.Host != "" && u.Host != "www.linkedin.com") || (u.Scheme != "" && u.Scheme != "https") || u.Path != "/messaging/compose/" || u.Query().Get("profileUrn") != target.ProfileURN || u.Query().Get("recipient") != strings.TrimPrefix(target.ProfileURN, "urn:li:fsd_profile:") {
				return
			}
			// Preserve the available action, without exporting LinkedIn's
			// suggested draft text or implying a free messaging entitlement.
			q := u.Query()
			q.Del("body")
			q.Del("subject")
			u.Scheme, u.Host, u.RawQuery = "https", "www.linkedin.com", q.Encode()
			appendProfileBlock(section, profileBlock{Text: label, Links: []profileLink{{Text: label, URL: u.String()}}})
		})
		return false
	})
	if section.Title != "Highlights" || len(section.Blocks) == 0 {
		return nil, nil
	}
	return section, nil
}

// Read selected React element content as data. Never traverse event handlers,
// dormant variants, tracking metadata, or execute serialized actions.
func walkProfileComponent(data documentData, value any, visit func(map[string]any) bool) {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		if ref, ok := value.(string); ok {
			if !strings.HasPrefix(ref, "$L") || seen[ref] {
				return
			}
			seen[ref] = true
			walk(data.resolve(ref))
			return
		}
		items, ok := value.([]any)
		if !ok {
			return
		}
		if len(items) == 4 && items[0] == "$" {
			props, ok := items[3].(map[string]any)
			if !ok || props["hidden"] == true || props["aria-hidden"] == true {
				return
			}
			selected := make(map[string]any, len(props)+1)
			for k, v := range props {
				selected[k] = v
			}
			if tag, ok := items[1].(string); ok && !strings.HasPrefix(tag, "$") {
				selected["tagName"] = tag
			}
			if !visit(selected) {
				return
			}
			if initial, ok := props["initialContent"]; ok {
				walk(initial)
			} else if text, ok := props["textProps"].(map[string]any); ok {
				walk(text["children"])
			} else {
				walk(props["children"])
			}
			return
		}
		for _, child := range items {
			walk(child)
		}
	}
	walk(value)
}

func profileComponentText(data documentData, value any) string {
	var pieces []string
	var text func(any, int)
	text = func(value any, depth int) {
		if depth > 64 {
			return
		}
		switch v := value.(type) {
		case string:
			if strings.HasPrefix(v, "$L") {
				text(data.resolve(v), depth+1)
			} else if !strings.HasPrefix(v, "$") {
				pieces = append(pieces, v)
			}
		case []any:
			if len(v) == 4 && v[0] == "$" {
				props, _ := v[3].(map[string]any)
				if props["hidden"] != true && props["aria-hidden"] != true {
					text(props["children"], depth+1)
				}
			} else {
				for _, child := range v {
					text(child, depth+1)
				}
			}
		}
	}
	text(value, 0)
	return strings.Join(strings.Fields(strings.Join(pieces, " ")), " ")
}
