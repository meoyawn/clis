package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

var pageScripts = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script\s*>`)
var streamedResult = regexp.MustCompile(`kind:\s*"Result\.Ok",\s*value:\s*`)

type pageIdentity struct {
	ID     string `json:"viewerId"`
	Handle string `json:"viewerScreenName"`
}

// The redesigned site streams JSON results inside router scripts. Decode only
// those string literals; never execute the router, page code, or telemetry.
func redesignedData(document []byte) (pageIdentity, []json.RawMessage, error) {
	var identity pageIdentity
	var results []json.RawMessage
	for _, script := range pageScripts.FindAllSubmatch(document, -1) {
		body := script[1]
		if _, tail, ok := bytes.Cut(body, []byte("window.__INITIAL_DATA__=")); ok {
			if err := json.NewDecoder(bytes.NewReader(tail)).Decode(&identity); err != nil {
				return identity, nil, fmt.Errorf("decode redesigned X account data: %w", err)
			}
		}
		for _, match := range streamedResult.FindAllIndex(body, -1) {
			var payload string
			if err := json.NewDecoder(bytes.NewReader(body[match[1]:])).Decode(&payload); err != nil {
				return identity, nil, fmt.Errorf("decode streamed X result: %w", err)
			}
			if !json.Valid([]byte(payload)) {
				return identity, nil, fmt.Errorf("streamed X result is not JSON")
			}
			results = append(results, json.RawMessage(payload))
		}
	}
	if identity.ID == "" || identity.Handle == "" {
		return identity, nil, fmt.Errorf("the redesigned X page does not expose a signed-in account; reimport the selected Chrome session")
	}
	if _, err := domain.Handle(identity.Handle); err != nil {
		return identity, nil, err
	}
	if len(results) == 0 {
		return identity, nil, fmt.Errorf("the redesigned X page contains no streamed JSON results")
	}
	return identity, results, nil
}

func (c *Client) bootstrapRedesigned(document []byte) (domain.Profile, error) {
	identity, results, err := redesignedData(document)
	if err != nil {
		return domain.Profile{}, err
	}
	if identity.ID != c.session.UserID() {
		return domain.Profile{}, fmt.Errorf("signed-in redesigned X account does not match selected Chrome cookie identity")
	}
	owner, err := profileInResults(results, identity.ID, identity.Handle)
	if err != nil {
		return domain.Profile{}, err
	}
	c.redesigned = true
	c.owner = owner
	c.bootstrapDocument = string(document)
	return owner, nil
}

func (c *Client) redesignedPage(ctx context.Context, path string) ([]json.RawMessage, error) {
	document, err := c.request(ctx, http.MethodGet, Origin+path, "", nil)
	if err != nil {
		if target := canonicalPostRedirect(path, err); target != "" {
			document, err = c.request(ctx, http.MethodGet, target, "", nil)
		}
	}
	if err != nil {
		return nil, err
	}
	identity, results, err := redesignedData(document)
	if err != nil {
		return nil, err
	}
	if identity.ID != c.owner.ID || !strings.EqualFold(identity.Handle, c.owner.Handle) {
		return nil, fmt.Errorf("the redesigned X page changed the selected account")
	}
	return results, nil
}

func canonicalPostRedirect(path string, failure error) string {
	var redirect *pageRedirect
	if !errors.As(failure, &redirect) {
		return ""
	}
	current, err := domain.NewPostRef(Origin + path)
	if err != nil {
		return ""
	}
	base, err := url.Parse(Origin + path)
	if err != nil {
		return ""
	}
	reference, err := url.Parse(redirect.location)
	if err != nil {
		return ""
	}
	target := base.ResolveReference(reference)
	if target.Host != "x.com" || target.String() == base.String() {
		return ""
	}
	canonical, err := domain.NewPostRef(target.String())
	if err != nil || canonical.ID() != current.ID() {
		return ""
	}
	return target.String()
}

func profileInResults(results []json.RawMessage, id, handle string) (domain.Profile, error) {
	var found *domain.Profile
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			if value["__typename"] == "User" && (id == "" || value["rest_id"] == id) {
				data, err := json.Marshal(value)
				if err != nil {
					return err
				}
				var user userRecord
				if err := json.Unmarshal(data, &user); err != nil {
					return err
				}
				if strings.EqualFold(user.Core.Handle, handle) {
					profile, err := user.profile()
					if err != nil {
						return err
					}
					if found != nil && found.ID != profile.ID {
						return fmt.Errorf("the redesigned X page contains conflicting identities for %q", handle)
					}
					found = &profile
				}
				return nil
			}
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, result := range results {
		var root any
		if err := json.Unmarshal(result, &root); err != nil {
			return domain.Profile{}, err
		}
		if err := walk(root); err != nil {
			return domain.Profile{}, err
		}
	}
	if found == nil {
		return domain.Profile{}, fmt.Errorf("the redesigned X page does not confirm profile %q", handle)
	}
	return *found, nil
}

func (c *Client) redesignedThread(ctx context.Context, ref domain.PostRef) ([]byte, error) {
	handle := ref.Handle()
	if handle == "" {
		handle = c.owner.Handle
	}
	results, err := c.redesignedPage(ctx, "/"+handle+"/status/"+ref.ID())
	if err != nil {
		return nil, err
	}
	var conversation, focal json.RawMessage
	for _, result := range results {
		var body struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(result, &body); err != nil {
			return nil, err
		}
		if value := body.Data["threaded_conversation_with_injections_v2"]; len(value) > 0 && string(value) != "null" {
			conversation = value
		}
		if value := body.Data["tweet_result_by_rest_id"]; len(value) > 0 && string(value) != "null" {
			focal = value
		}
	}
	if len(conversation) == 0 {
		if len(focal) == 0 {
			return nil, fmt.Errorf("redesigned X page has no conversation for post %q", ref.ID())
		}
		return focal, nil
	}
	// The SSR document can repeat the focal tweet in a separate preload result.
	// Keep the conversation's copy and use the preload only if it is absent.
	records, err := tweetRecords(conversation)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.ID == ref.ID() {
			return conversation, nil
		}
	}
	return json.Marshal([]json.RawMessage{conversation, focal})
}

func (c *Client) redesignedTimeline(ctx context.Context, path string) ([]byte, error) {
	results, err := c.redesignedPage(ctx, path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(results)
}
