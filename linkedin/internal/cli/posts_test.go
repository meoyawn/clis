package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
	"github.com/meoyawn/clis/linkedin/internal/service"
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
		{name: "numeric activity payload receipt", publish: true, receipt: `0:{"response":{"errors":[],"completionAction":{"actions":[{"value":{"requestId":"com.linkedin.sdui.sharing.requests.FetchSharingAfterPostNBA","requestedArguments":{"payload":{"activityId":"123456789"}}}}]}}}`, document: postDocument, wantPosts: 1},
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

func TestNativeImagePublicationLifecycle(t *testing.T) {
	const asset = "SyntheticImageAsset1"
	for _, tc := range []struct {
		name                                                    string
		preview, foreignUpload, wrongRegistration, missingImage bool
		wantWrites                                              int
	}{
		{name: "preview never uploads", preview: true},
		{name: "approved bytes attached and independently verified", wantWrites: 4},
		{name: "foreign upload destination rejected", foreignUpload: true, wantWrites: 1},
		{name: "registration must confirm the same asset", wrongRegistration: true, wantWrites: 3},
		{name: "readback must contain the uploaded asset", missingImage: true, wantWrites: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var imageBytes bytes.Buffer
			if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 1))); err != nil {
				t.Fatal(err)
			}
			imagePath := filepath.Join(dir, "illustration.png")
			if err := os.WriteFile(imagePath, imageBytes.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			img, err := service.LoadPostImage(imagePath)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := domain.NewPostDraft("Line one\n\nRead https://example.com/article")
			if err != nil {
				t.Fatal(err)
			}
			opts := postCreateOptions{publish: !tc.preview, expectedAuthor: postAuthorURL, expectedSHA256: postContentHash(draft, img), recordDir: filepath.Join(dir, "history"), image: img}
			writes := []string{}
			client := linkedinClient{session: session{LIAT: "test", JSessionID: "ajax:test"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				if r.Method == http.MethodGet {
					switch r.URL.Path {
					case "/voyager/api/me":
						return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
					case "/voyager/api/identity/dash/profiles":
						return jsonResponse([]byte(`{"data":{"*elements":["urn:li:fsd_profile:ACoSelf"]},"included":[{"entityUrn":"urn:li:fsd_profile:ACoSelf","publicIdentifier":"example-author","firstName":"Example","lastName":"Author"}]}`)), nil
					case "/feed/update/urn:li:activity:123456789/":
						doc := postDocument
						if !tc.missingImage {
							doc = strings.Replace(doc, "<h2>", `<img src="https://media.licdn.com/dms/image/v2/`+asset+`/feedshare/0/image"><h2>`, 1)
						}
						return jsonResponse([]byte(doc)), nil
					}
				}
				// A durable private attempt must precede the first upload or post.
				entries, err := os.ReadDir(opts.recordDir)
				if err != nil || len(entries) == 0 {
					t.Fatalf("mutation without journal: %v", err)
				}
				info, err := entries[0].Info()
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("journal permissions: %v", err)
				}
				if r.Method == http.MethodPut {
					writes = append(writes, "upload")
					data, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(data, imageBytes.Bytes()) || !strings.Contains(r.URL.Path, "/"+asset+"/") || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("mediaTypeFamily") != "STILLIMAGE" {
						t.Fatalf("upload changed approved bytes or target: %s", r.URL)
					}
					resp := jsonResponse(nil)
					resp.StatusCode = http.StatusCreated
					return resp, nil
				}
				if r.Method != http.MethodPost {
					return nil, fmt.Errorf("unexpected %s %s", r.Method, r.URL)
				}
				action := r.URL.Query().Get("sduiid")
				writes = append(writes, action)
				var body struct {
					States []struct {
						Key   string
						Value any
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				states := map[string]any{}
				for _, s := range body.States {
					states[s.Key] = s.Value
				}
				switch action {
				case "com.linkedin.sdui.requests.media.registerMediaUpload":
					host := "www.linkedin.com"
					if tc.foreignUpload {
						host = "foreign.example"
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"upload":{"$type":"proto.sdui.actions.core.media.UploadInstruction","assetUrn":"` + asset + `","instruction":{"$type":"proto.com.linkedin.digitalmedia.ingest.SingleRequestUploadInstruction","uploadUrl":"https://` + host + `/dms-uploads/sp/sync/v2/` + asset + `/uploaded-image/1/0?signature=synthetic","headers":[{"$type":"proto.com.linkedin.digitalmedia.ingest.Header","key":"mediaTypeFamily","value":"STILLIMAGE"}]}}}}}`)), nil
				case "com.linkedin.sdui.requests.mediaproduct.RegisterImageMedia":
					ids, _ := states["media-creation-editor-imageAssetIds"].([]any)
					if len(ids) != 1 || ids[0] != asset {
						t.Fatal("registration did not use uploaded asset")
					}
					registered := asset
					if tc.wrongRegistration {
						registered = "AnotherImageAsset1"
					}
					return jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"$type":"proto.sdui.actions.core.SetState","value":{"state":{"key":{"key":{"value":{"id":"ShareBoxassetIdBinding"}}},"value":{"stringValue":"` + registered + `"}}}}}}`)), nil
				case "com.linkedin.sdui.requests.sharing.CreatePost":
					if len(writes) != 4 || states["ShareBoxassetIdBinding"] != asset || states["ShareBoxdetourType"] != "Image" || states["ShareBoxrichCommentaryTextMemoryBinding"].(map[string]any)["text"] != draft.Text() {
						t.Fatal("final post lost approved image or text")
					}
					return jsonResponse([]byte(postReceipt)), nil
				default:
					return nil, fmt.Errorf("unexpected action %s", action)
				}
			})}
			result, err := client.createPost(context.Background(), draft, opts)
			wantError := tc.foreignUpload || tc.wrongRegistration || tc.missingImage
			if (err != nil) != wantError || len(writes) != tc.wantWrites {
				t.Fatalf("writes=%v result=%+v err=%v", writes, result, err)
			}
			if tc.preview {
				return
			}
			before := len(writes)
			duplicate, duplicateErr := client.createPost(context.Background(), draft, opts)
			if len(writes) != before || (duplicateErr != nil) != wantError {
				t.Fatalf("duplicate wrote again or lost uncertain guard: %v", duplicateErr)
			}
			if !wantError && (!result.Verified || !result.Published || !duplicate.AlreadyPresent) {
				t.Fatalf("unverified image publication: %+v", result)
			}
		})
	}
}
