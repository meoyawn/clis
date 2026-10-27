package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

// Synthetic response preserves the observed zero-length binary record followed
// immediately by the form's model states and both existing-media and new-media Save branches.
func syntheticEditForm(text, target string) []byte {
	state := func(key, kind string, value any) any {
		return map[string]any{"key": map[string]any{"key": map[string]any{"value": map[string]any{"id": key}}}, "value": map[string]any{"$case": kind, kind: value}}
	}
	rows := map[string]any{"modelStates": []any{
		state("ShareBoxshareBoxMode", "stringValue", "Edit"),
		state("ShareBoxselectedActor", "stringValue", "Self"),
		state("ShareBoxeditingPostUrnBinding", "stringValue", target),
		state("ShareBoxdetourType", "stringValue", "Image"),
		state("ShareBoxshareLifeCycleState", "stringValue", "ShareLifeCycleState_UNKNOWN"),
		state("media-creation-editor-pickedMedia", "mediaListValue", map[string]any{"analysisData": "$10"}),
		state("ShareBoxassetIdBinding", "stringValue", "SyntheticImageAsset1"),
		state("ShareBoxrichCommentaryTextMemoryBinding", "textModelForWrite", map[string]any{"text": text}),
	}, "existingMediaAction": map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{"requestId": "com.linkedin.sdui.requests.sharing.LaunchUpdatePostWithMedia", "requestedArguments": map[string]any{"requestedStateKeys": []any{}}}}, "action": map[string]any{"$type": "proto.sdui.actions.core.ServerRequest", "value": map[string]any{"requestId": "com.linkedin.sdui.requests.sharing.DispatchPostUpdateWithDetour", "requestedArguments": map[string]any{"requestedStateKeys": []any{map[string]any{"key": map[string]any{"value": map[string]any{"id": "ShareBoxdetourType"}}}}}}}}
	data, err := json.Marshal(rows)
	if err != nil {
		panic(err)
	}
	return []byte("10:o0,a:" + string(data) + "\n")
}

func TestPostEditPreservesTargetTextAndImage(t *testing.T) {
	const original = "Line one\n\nRead https://example.com/article"
	const replacement = "Updated text\n\nRead https://example.com/article"
	ref, err := domain.NewPostRef(postPermalink)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := domain.NewPostDraft(replacement)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                                              string
		preview, stale, wrongAuthor, timeout, wrongReadback, removedImage bool
		wantPosts                                                         int
	}{
		{name: "preview is read only", preview: true},
		{name: "edit and reconcile exact saved text", wantPosts: 2},
		{name: "concurrent text change refused", stale: true},
		{name: "different account refused", wrongAuthor: true},
		{name: "timeout is never retried", timeout: true, wantPosts: 1},
		{name: "wrong saved text fails verification", wrongReadback: true, wantPosts: 2},
		{name: "image disappearance fails verification", removedImage: true, wantPosts: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := domain.Post{URL: postPermalink, URN: ref.URN(), AuthorURL: postAuthorURL, Text: original, ImageAssetIDs: []string{"SyntheticImageAsset1"}}
			plan, err := preparePostEdit(before, draft, syntheticEditForm(original, ref.URN()))
			if err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(original)))
			opts := postEditOptions{apply: !tc.preview, expectedAuthor: postAuthorURL, expectedHash: draft.SHA256(), expectedCurrentHash: hash, recordDir: filepath.Join(t.TempDir(), "edits")}
			if tc.stale {
				opts.expectedCurrentHash = strings.Repeat("0", 64)
			}
			if tc.wrongAuthor {
				opts.expectedAuthor = "https://www.linkedin.com/in/other/"
			}
			posts := 0
			client := linkedinClient{session: session{LIAT: "synthetic", JSessionID: "ajax:synthetic"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				if r.Method == http.MethodGet && r.URL.String() == postPermalink {
					doc := strings.Replace(postDocument, "Line one", "Updated text", 1)
					if tc.wrongReadback {
						doc = postDocument
					}
					if !tc.removedImage {
						doc = strings.Replace(doc, "<h2>", `<img src="https://media.licdn.com/dms/image/v2/SyntheticImageAsset1/feedshare/0/image"><h2>`, 1)
					}
					return jsonResponse([]byte(doc)), nil
				}
				if r.Method != http.MethodPost {
					return nil, fmt.Errorf("unexpected request %s", r.URL)
				}
				posts++
				if tc.timeout {
					return nil, context.DeadlineExceeded
				}
				var body struct {
					States []struct {
						Key, Namespace string
						Value          any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if posts == 1 {
					if r.URL.Query().Get("sduiid") != "com.linkedin.sdui.requests.sharing.LaunchUpdatePostWithMedia" || len(body.States) != 0 {
						t.Fatal("save did not select the existing-image branch")
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"$type":"proto.sdui.actions.core.ServerRequest","value":{"requestId":"com.linkedin.sdui.sharing.requests.UpdateNormShare","requestedArguments":{"requestedStateKeys":[{"key":{"value":{"id":"ShareBoxeditingPostUrnBinding"}}},{"key":{"value":{"id":"ShareBoxrichCommentaryTextMemoryBinding"}}},{"key":{"value":{"id":"ShareBoxshareLifeCycleState"}}},{"key":{"value":{"id":"media-creation-editor-pickedMedia"}}},{"key":{"value":{"id":"uninitialized"}}}]}}}]}}}`)), nil
				}
				if r.URL.Query().Get("sduiid") != "com.linkedin.sdui.sharing.requests.UpdateNormShare" || len(body.States) != 4 || body.States[2].Value != "ShareLifeCycleState_PUBLISHED" || body.States[0].Value != ref.URN() || jsonStringAt(body.States[1].Value, "text") != replacement {
					t.Fatal("update changed target, replacement text, or lifecycle")
				}
				if _, ok := body.States[3].Value.(map[string]any)["analysisData"].(map[string]any); !ok {
					t.Fatal("image binary metadata was not preserved")
				}
				return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[{"$type":"proto.sdui.actions.core.ServerRequest","value":{"requestId":"refresh-feed-after-save"}}]}}}`)), nil
			})}
			result, err := client.applyPostEdit(context.Background(), ref, person{URL: postAuthorURL}, before, draft, plan, opts)
			wantError := tc.stale || tc.wrongAuthor || tc.timeout || tc.wrongReadback || tc.removedImage
			if (err != nil) != wantError || posts != tc.wantPosts {
				t.Fatalf("posts=%d result=%+v error=%v", posts, result, err)
			}
			if tc.preview {
				if result.Edited || result.Verified {
					t.Fatal("preview claimed an edit")
				}
				return
			}
			if posts == 0 {
				return
			}
			prior := posts
			secondBefore := before
			if !wantError {
				secondBefore.Text = replacement
			}
			second, secondErr := client.applyPostEdit(context.Background(), ref, person{URL: postAuthorURL}, secondBefore, draft, plan, opts)
			if posts != prior || (secondErr != nil) != wantError {
				t.Fatalf("retry changed state: posts=%d err=%v", posts, secondErr)
			}
			if !wantError && (!result.Edited || !result.Verified || !second.AlreadyPresent) {
				t.Fatal("edit/readback was not verified")
			}
		})
	}
	before := domain.Post{URN: ref.URN(), Text: original, ImageAssetIDs: []string{"SyntheticImageAsset1"}}
	if _, err := preparePostEdit(before, draft, syntheticEditForm(original, "urn:li:activity:999")); err == nil {
		t.Fatal("foreign edit form accepted")
	}
}

func TestPostShortLinkResolvesWithoutLeakingCookies(t *testing.T) {
	for _, tc := range []struct {
		name, destination string
		wantError         bool
	}{
		{name: "canonical activity", destination: postPermalink},
		{name: "share permalink is read for actual activity", destination: "https://www.linkedin.com/posts/example_share-987654321-abcd/"},
		{name: "foreign redirect refused", destination: "https://example.com/steal", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := linkedinClient{session: session{LIAT: "secret", JSessionID: "secret"}}
			c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "lnkd.in" {
					if r.Header.Get("Cookie") != "" || r.Header.Get("Csrf-Token") != "" {
						t.Fatal("credentials sent to shortener")
					}
					resp := jsonResponse(nil)
					resp.StatusCode = http.StatusFound
					resp.Header.Set("Location", tc.destination)
					return resp, nil
				}
				if r.URL.Host != "www.linkedin.com" {
					t.Fatal("followed foreign redirect")
				}
				return jsonResponse([]byte(postDocument)), nil
			})}
			ref, err := c.resolvePost(context.Background(), "https://lnkd.in/p/example")
			if (err != nil) != tc.wantError || (!tc.wantError && ref.URL() != postPermalink) {
				t.Fatalf("ref=%v err=%v", ref, err)
			}
		})
	}
}
