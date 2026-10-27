package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

const postAuthorURL = "https://www.linkedin.com/in/example-author/"
const postPermalink = "https://www.linkedin.com/feed/update/urn:li:activity:123456789/"
const postReceipt = `0:{"response":{"errors":[],"completionAction":{"actions":[{"navigate":{"url":"https://www.linkedin.com/feed/update/urn:li:activity:123456789/"}}]}}}`

// Synthetic HTML retains the observed sibling relationship: the activity marker
// is outside the focus card. Sidebar and comments must not supply author/text.
const postDocument = `<html><body><aside><a href="https://www.linkedin.com/in/wrong-author/">Wrong Author</a></aside>
<section aria-label="Primary content"><div role="listitem" componentkey="update-card-focusSynthetic">
<h2>Feed post</h2><a href="https://www.linkedin.com/in/example-author/">Example Author</a>
<span data-testid="expandable-text-box">Line one<br><br>Read https://example.com/article</span></div>
<div data-testid="ReactionFacepileCollection-urn:li:activity:123456789"></div>
<div componentkey="replaceableComment_other"><a href="https://www.linkedin.com/in/commenter/">Commenter</a><p>A comment</p></div>
</section></body></html>`

func TestPostPreviewPublicationAndDuplicateProtection(t *testing.T) {
	for _, tc := range []struct {
		name, receipt, document, expectedAuthor, expectedHash string
		publish, transportError                               bool
		status, wantPosts                                     int
		wantError                                             bool
	}{
		{name: "preview is read only"},
		{name: "approved publication and durable duplicate guard", publish: true, receipt: postReceipt, document: postDocument, wantPosts: 1},
		{name: "author changed", publish: true, expectedAuthor: "https://www.linkedin.com/in/another/", wantError: true},
		{name: "text changed", publish: true, expectedHash: strings.Repeat("0", 64), wantError: true},
		{name: "timeout is not retried", publish: true, transportError: true, wantPosts: 1, wantError: true},
		{name: "rate limit stops", publish: true, status: 429, wantPosts: 1, wantError: true},
		{name: "authentication stops", publish: true, status: 403, wantPosts: 1, wantError: true},
		{name: "HTTP success without receipt is unconfirmed", publish: true, receipt: `0:{"response":{"errors":[],"completionAction":{"actions":[{}]}}}`, wantPosts: 1, wantError: true},
		{name: "completion with errors is unconfirmed", publish: true, receipt: `0:{"response":{"errors":[{"message":"rejected"}],"completionAction":{"url":"` + postPermalink + `"}}}`, wantPosts: 1, wantError: true},
		{name: "different activity in readback", publish: true, receipt: postReceipt, document: strings.ReplaceAll(postDocument, "activity:123456789", "activity:777"), wantPosts: 1, wantError: true},
		{name: "different author in readback", publish: true, receipt: postReceipt, document: strings.ReplaceAll(postDocument, "in/example-author/", "in/another/"), wantPosts: 1, wantError: true},
		{name: "different text in readback", publish: true, receipt: postReceipt, document: strings.ReplaceAll(postDocument, "Line one", "Another post"), wantPosts: 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "Line one\n\nRead https://example.com/article"
			draft, err := domain.NewPostDraft(text)
			if err != nil {
				t.Fatal(err)
			}
			var posts int
			client := linkedinClient{session: session{LIAT: "test", JSessionID: "ajax:test"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/voyager/api/me":
					return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
				case r.Method == http.MethodGet && r.URL.Path == "/voyager/api/identity/dash/profiles":
					return jsonResponse([]byte(`{"data":{"*elements":["urn:li:fsd_profile:ACoSelf"]},"included":[{"entityUrn":"urn:li:fsd_profile:ACoSelf","publicIdentifier":"example-author","firstName":"Example","lastName":"Author"}]}`)), nil
				case r.Method == http.MethodGet && r.URL.String() == postPermalink:
					return jsonResponse([]byte(tc.document)), nil
				case r.Method == http.MethodPost:
					posts++
					if r.URL.Path != "/flagship-web/rsc-action/actions/server-request" || r.URL.Query().Get("sduiid") != "com.linkedin.sdui.requests.sharing.CreatePost" {
						t.Fatalf("unexpected publication endpoint %s", r.URL)
					}
					var request struct {
						States []struct {
							Key   string
							Value any
						}
						RequestedArguments struct{ States []json.RawMessage }
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					states := map[string]any{}
					for _, s := range request.States {
						states[s.Key] = s.Value
					}
					if states["ShareBoxrichCommentaryTextMemoryBinding"].(map[string]any)["text"] != text || states["ShareBoxselectedVisibilitySettingString"] != "Anyone" || states["ShareBoxselectedActor"] != "Self" || states["ShareBoxmediaUrnBinding"] != "" || len(request.RequestedArguments.States) != len(request.States) {
						t.Fatal("publication changed the reviewed text, actor, visibility or media")
					}
					if tc.transportError {
						return nil, context.DeadlineExceeded
					}
					response := jsonResponse([]byte(tc.receipt))
					if tc.status != 0 {
						response.StatusCode = tc.status
					}
					return response, nil
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
			})}
			opts := postCreateOptions{publish: tc.publish, expectedAuthor: postAuthorURL, expectedSHA256: draft.SHA256(), recordDir: filepath.Join(t.TempDir(), "history")}
			if tc.expectedAuthor != "" {
				opts.expectedAuthor = tc.expectedAuthor
			}
			if tc.expectedHash != "" {
				opts.expectedSHA256 = tc.expectedHash
			}
			result, err := client.createPost(context.Background(), draft, opts)
			if (err != nil) != tc.wantError || posts != tc.wantPosts {
				t.Fatalf("posts=%d result=%+v err=%v", posts, result, err)
			}
			if !tc.publish {
				if result.Published || result.Verified || result.Text != text || result.Author.URL != postAuthorURL {
					t.Fatalf("bad preview: %+v", result)
				}
				if _, err := os.Stat(opts.recordDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("preview wrote history: %v", err)
				}
				return
			}
			if posts == 0 {
				return
			}
			before := posts
			duplicate, duplicateErr := client.createPost(context.Background(), draft, opts)
			if posts != before {
				t.Fatal("second invocation repeated a publish POST")
			}
			if tc.wantError {
				if duplicateErr == nil || duplicate.Published {
					t.Fatal("unconfirmed attempt did not block a retry")
				}
				return
			}
			if !result.Published || !result.Verified || result.PostURL != postPermalink || duplicateErr != nil || !duplicate.AlreadyPresent {
				t.Fatalf("publication not verified or deduplicated: %+v %+v %v", result, duplicate, duplicateErr)
			}
			info, err := os.Stat(result.RecordPath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("history is not private: %v", err)
			}
		})
	}
}

func TestPostInputRejectedBeforeAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		args       []string
		want       string
	}{
		{name: "blank", text: " \n", want: "nonempty"},
		{name: "invalid encoding", text: string([]byte{0xff}), want: "UTF-8"},
		{name: "UTF-16 limit counts emoji twice", text: strings.Repeat("😀", 1501), want: "3002"},
		{name: "approval missing", text: "A post", args: []string{"--publish"}, want: "--expect-author"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "post.txt")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"create", "--text-file", path}, tc.args...)
			var out bytes.Buffer
			err := runPosts(context.Background(), args, authStore{dir: filepath.Join(t.TempDir(), "no-auth")}, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 {
				t.Fatalf("validation: output=%q err=%v", out.String(), err)
			}
		})
	}
}
