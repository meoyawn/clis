package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

const userJSON = `{"__typename":"User","rest_id":"42","core":{"name":"Example Author","screen_name":"example"},"relationship_counts":{"followers":12}}`
const tweetJSON = `{"__typename":"Tweet","rest_id":"123","core":{"user_results":{"result":` + userJSON + `}},"legacy":{"id_str":"123","user_id_str":"42","full_text":"Keep the session running https://t.co/example https://t.co/media","created_at":"Tue Oct 27 10:00:00 +0000 2026","favorite_count":1,"reply_count":0,"retweet_count":0,"quote_count":0,"bookmark_count":0,"entities":{"urls":[{"url":"https://t.co/example","expanded_url":"https://example.com/article"}]},"extended_entities":{"media":[{"id_str":"456","url":"https://t.co/media"}]}},"views":{"count":"9"}}`

// This boundary test owns the publish lifecycle: read-only preview, exact image
// upload, independent readback, and a private durable guard across invocations.
// Responses are synthetic and follow the inspected browser schema.
func TestPostPackageLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name                                                                                                                   string
		status                                                                                                                 int
		timeout, wrongReadback, receiptError, reply, minimalReceipt, idOnlyReceipt, legacyOnlyReceipt, wrongMedia, wrongParent bool
	}{
		{name: "verified image publication and duplicate no-op"},
		{name: "own reply retains exact parent", reply: true},
		{name: "create receipt omits duplicate legacy identifiers", minimalReceipt: true},
		{name: "receipt contains only a post identifier", idOnlyReceipt: true},
		{name: "receipt uses legacy post identifier", legacyOnlyReceipt: true},
		{name: "timeout blocks another attempt", timeout: true},
		{name: "rate limit stops", status: 429},
		{name: "authentication stops", status: 403},
		{name: "HTTP success with API error", receiptError: true},
		{name: "wrong readback is unconfirmed", wrongReadback: true},
		{name: "wrong image readback is unconfirmed", wrongMedia: true},
		{name: "reply parent must match", reply: true, wrongParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// A synthetic PNG signature is sufficient for MIME detection;
			// the mock verifies the exact file bytes, not an encoded replacement.
			imageData := []byte("\x89PNG\r\n\x1a\nsynthetic-image-content")
			imagePath := filepath.Join(dir, "illustration.png")
			if err := os.WriteFile(imagePath, imageData, 0600); err != nil {
				t.Fatal(err)
			}
			image, err := service.LoadImage(imagePath)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := domain.NewDraft("Keep the session running https://example.com/article")
			if err != nil {
				t.Fatal(err)
			}
			posts, uploads, gets := 0, 0, 0
			wireTweet := tweetJSON
			if tc.reply {
				wireTweet = strings.Replace(wireTweet, `"legacy":{`, `"legacy":{"in_reply_to_status_id_str":"122",`, 1)
			}
			var packagePath string
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "abs.twimg.com" {
					if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
						t.Fatal("account secrets leaked to public asset host")
					}
					bundle := `const token="AAAAA` + strings.Repeat("a", 70) + `";`
					for _, op := range []string{"UserByScreenName", "TweetDetail", "CreateTweet", "UserOriginalsTimeline"} {
						bundle += `({queryId:"test-query",operationName:"` + op + `"});`
					}
					return response(200, bundle), nil
				}
				if r.Method == http.MethodGet {
					gets++
					switch {
					case r.URL.Path == "/home":
						return response(200, `<script src="https://abs.twimg.com/responsive-web/client-web/main.fixture.js"></script><script>window.__INITIAL_STATE__={"settings":{"remote":{"settings":{"screen_name":"example"}}}};</script>`), nil
					case strings.HasSuffix(r.URL.Path, "/UserByScreenName"):
						return response(200, `{"data":{"user":{"result":`+userJSON+`}}}`), nil
					case strings.HasSuffix(r.URL.Path, "/TweetDetail"):
						body := wireTweet
						if tc.wrongReadback {
							body = strings.Replace(body, "Keep the session running", "Another post", 1)
						}
						if tc.wrongMedia {
							body = strings.Replace(body, `"id_str":"456"`, `"id_str":"789"`, 1)
						}
						if tc.wrongParent {
							body = strings.Replace(body, `"in_reply_to_status_id_str":"122"`, `"in_reply_to_status_id_str":"777"`, 1)
						}
						return response(200, `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"entries":[{"content":{"itemContent":{"tweet_results":{"result":`+body+`}}}}]}]}}}`), nil
					default:
						return nil, fmt.Errorf("unexpected GET %s", r.URL.Path)
					}
				}
				// The attempt must be persisted before even uploading an image.
				data, err := os.ReadFile(packagePath)
				if err != nil {
					t.Fatalf("POST before private attempt: %v", err)
				}
				var attempt packagePreview
				if err := json.Unmarshal(data, &attempt); err != nil || attempt.Published {
					t.Fatal("attempt did not record unconfirmed state")
				}
				if r.URL.Host == "upload.x.com" {
					uploads++
					switch r.URL.Query().Get("command") {
					case "INIT":
						if r.URL.Query().Get("total_bytes") != fmt.Sprint(len(imageData)) || r.URL.Query().Get("media_type") != "image/png" || r.URL.Query().Get("media_category") != "tweet_image" {
							t.Fatal("image INIT changed the selected file")
						}
						return response(200, `{"media_id_string":"456"}`), nil
					case "APPEND":
						_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
						if err != nil {
							t.Fatal(err)
						}
						part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
						if err != nil {
							t.Fatal(err)
						}
						got, err := io.ReadAll(part)
						if err != nil || part.FormName() != "media" || !bytes.Equal(got, imageData) {
							t.Fatal("uploaded image bytes changed")
						}
						return response(204, ""), nil
					case "FINALIZE":
						return response(200, `{"media_id_string":"456","image":{"w":1000,"h":610}}`), nil
					default:
						t.Fatalf("unexpected upload command %q", r.URL.Query().Get("command"))
					}
				}
				if !strings.HasSuffix(r.URL.Path, "/CreateTweet") {
					t.Fatalf("unexpected mutation %s", r.URL.Path)
				}
				posts++
				wantReferer := "https://x.com/example"
				if tc.reply {
					wantReferer += "/status/122"
				}
				if r.Header.Get("Referer") != wantReferer {
					t.Fatalf("wrong publishing context %q", r.Header.Get("Referer"))
				}
				var body struct {
					Variables struct {
						Text  string          `json:"tweet_text"`
						Reply json.RawMessage `json:"reply"`
						Media struct {
							Entities []struct {
								ID string `json:"media_id"`
							} `json:"media_entities"`
						} `json:"media"`
					} `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Variables.Text != draft.Text() || len(body.Variables.Media.Entities) != 1 || body.Variables.Media.Entities[0].ID != "456" {
					t.Fatal("create request changed reviewed text/media/reply")
				}
				if tc.reply {
					var reply struct {
						Parent string `json:"in_reply_to_tweet_id"`
					}
					if err := json.Unmarshal(body.Variables.Reply, &reply); err != nil || reply.Parent != "122" {
						t.Fatal("reply destination changed")
					}
				} else if len(body.Variables.Reply) != 0 {
					t.Fatal("ordinary post became a reply")
				}
				if tc.timeout {
					return nil, context.DeadlineExceeded
				}
				if tc.status != 0 {
					return response(tc.status, `{"errors":[{"code":88}]}`), nil
				}
				if tc.receiptError {
					return response(200, `{"errors":[{"code":187,"message":"duplicate"}]}`), nil
				}
				receiptTweet := wireTweet
				if tc.minimalReceipt {
					receiptTweet = strings.ReplaceAll(receiptTweet, `"id_str":"123",`, "")
					receiptTweet = strings.ReplaceAll(receiptTweet, `"user_id_str":"42",`, "")
				}
				if tc.idOnlyReceipt {
					receiptTweet = `{"rest_id":"123"}`
				}
				if tc.legacyOnlyReceipt {
					receiptTweet = `{"legacy":{"id_str":"123"}}`
				}
				return response(200, `{"data":{"create_tweet":{"tweet_results":{"result":`+receiptTweet+`}}}}`), nil
			})
			client := service.NewClient(&http.Client{Transport: transport}, service.Session{AuthToken: "synthetic-auth", CSRF: "synthetic-csrf", TWID: "u%3D42"})
			owner, err := client.Bootstrap(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var parent *domain.Post
			if tc.reply {
				parent = &domain.Post{ID: "122", Author: owner, URL: "https://x.com/example/status/122"}
			}
			p := preview(owner, draft, image, parent)
			if p.Published || p.Verified || p.Text != draft.Text() || p.Image.SHA256 != image.SHA256 || posts != 0 || uploads != 0 || gets != 2 {
				t.Fatalf("preview performed writes or lost reviewed values: %+v", p)
			}
			packagePath = filepath.Join(dir, "posts", p.SHA256+".json")
			var out bytes.Buffer
			err = publishPackage(context.Background(), client, draft, image, parent, dir, p, &out)
			wantError := tc.timeout || tc.status != 0 || tc.receiptError || tc.wrongReadback || tc.wrongMedia || tc.wrongParent
			if (err != nil) != wantError || posts != 1 || uploads != 3 {
				t.Fatalf("posts=%d uploads=%d err=%v output=%s", posts, uploads, err, out.String())
			}
			if !tc.timeout {
				receiptInfo, receiptErr := os.Stat(packagePath + ".receipt.json")
				if receiptErr != nil || receiptInfo.Mode().Perm() != 0600 || receiptInfo.Size() == 0 {
					t.Fatalf("receipt was not preserved privately: %v", receiptErr)
				}
			}
			beforeUploads := uploads
			beforePosts := posts
			var duplicate bytes.Buffer
			again := publishPackage(context.Background(), client, draft, image, parent, dir, p, &duplicate)
			if uploads != beforeUploads || posts != beforePosts {
				t.Fatal("second invocation repeated a mutation")
			}
			if wantError {
				if again == nil || duplicate.Len() != 0 {
					t.Fatal("uncertain publication was retried or reported successful")
				}
				return
			}
			if again != nil {
				t.Fatal(again)
			}
			var got packagePreview
			if err := json.Unmarshal(duplicate.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !got.Published || !got.Verified || !got.AlreadyPresent || got.Post.URL != "https://x.com/example/status/123" || got.Post.Views == nil || *got.Post.Views != 9 {
				t.Fatalf("publication not verified: %+v", got)
			}
			info, err := os.Stat(packagePath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("attempt record is not private: %v", err)
			}
		})
	}
}

func TestInvalidPostNeverLoadsAccount(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		args       []string
		want       string
	}{
		{name: "blank", text: "\n", want: "nonempty"},
		{name: "over limit", text: strings.Repeat("x", 281), want: "281"},
		{name: "CJK weight", text: strings.Repeat("界", 141), want: "282"},
		{name: "missing reviewed hash", text: "Hello", args: []string{"--publish"}, want: "--expect-author"},
		{name: "bad reply target", text: "Hello", args: []string{"--reply-to", "https://evil.example/42"}, want: "invalid post reference"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "post.txt")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := create(context.Background(), append([]string{"--text-file", path}, tc.args...), dir, &out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 {
				t.Fatalf("expected %q before auth, got %q, %v", tc.want, out.String(), err)
			}
			if _, err := os.Stat(filepath.Join(dir, "posts")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input wrote history")
			}
		})
	}
}
