package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

func TestOwnCommentPublicationLifecycle(t *testing.T) {
	const text = "Read https://example.com/article\nMore detail"
	const card = `<div componentkey="replaceableComment_urn:li:comment:(activity:123456789,456)"><a href="https://www.linkedin.com/in/example-author/">Example Author</a><div componentkey="translatable-commentary-FeTranslationUrn(commentId=456, thread=urn:li:activity:123456789)"><span data-testid="expandable-text-box">Read <a href="https://www.linkedin.com/safety/go/?url=https%3A%2F%2Fexample.com%2Farticle">https://lnkd.in/synthetic</a><br>More detail</span></div></div>`
	for _, tc := range []struct {
		name                                                                                                           string
		preview, foreignParent, changedApproval, timeout, rateLimit, wrongAuthor, wrongText, wrongParent, untranslated bool
	}{
		{name: "read only preview", preview: true},
		{name: "approved link comment and independent readback"},
		{name: "untranslated comment has no translation wrapper", untranslated: true},
		{name: "foreign parent denied", foreignParent: true},
		{name: "changed approval denied", changedApproval: true},
		{name: "timeout blocks retry", timeout: true},
		{name: "rate limit stops", rateLimit: true},
		{name: "wrong comment author is unconfirmed", wrongAuthor: true},
		{name: "wrong comment text is unconfirmed", wrongText: true},
		{name: "comment under another activity is unconfirmed", wrongParent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := domain.NewPostRef(postPermalink)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := domain.NewPostDraft(text)
			if err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(postAuthorURL+"\x00"+ref.URN()+"\x00"+text)))
			opts := commentOptions{publish: !tc.preview, expectedAuthor: postAuthorURL, expectedHash: hash, recordDir: filepath.Join(t.TempDir(), "comments")}
			if tc.changedApproval {
				opts.expectedHash = strings.Repeat("0", 64)
			}
			postCount := 0
			client := linkedinClient{session: session{LIAT: "test", JSessionID: "ajax:test"}}
			client.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				client.lastRequestAt = time.Time{}
				switch r.URL.Path {
				case "/voyager/api/me":
					return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
				case "/voyager/api/identity/dash/profiles":
					return jsonResponse([]byte(`{"data":{"*elements":["urn:li:fsd_profile:ACoSelf"]},"included":[{"entityUrn":"urn:li:fsd_profile:ACoSelf","publicIdentifier":"example-author","firstName":"Example","lastName":"Author"}]}`)), nil
				case "/feed/update/urn:li:activity:123456789/":
					if r.Method != http.MethodGet {
						t.Fatal("readback was not GET")
					}
					doc := postDocument
					if tc.foreignParent {
						doc = strings.ReplaceAll(doc, "in/example-author/", "in/foreign/")
					}
					if postCount > 0 && !tc.rateLimit && !tc.timeout {
						comment := card
						if tc.untranslated {
							comment = strings.Replace(comment, ` componentkey="translatable-commentary-FeTranslationUrn(commentId=456, thread=urn:li:activity:123456789)"`, "", 1)
						}
						if tc.wrongAuthor {
							comment = strings.ReplaceAll(comment, "in/example-author/", "in/foreign/")
						}
						if tc.wrongText {
							comment = strings.ReplaceAll(comment, "More detail", "Unapproved text")
						}
						if tc.wrongParent {
							comment = strings.ReplaceAll(comment, "123456789", "987654321")
						}
						doc = strings.Replace(doc, "</section>", comment+"</section>", 1)
					}
					return jsonResponse([]byte(doc)), nil
				case "/flagship-web/rsc-action/actions/server-request":
					if r.Method != http.MethodPost || r.URL.Query().Get("sduiid") != "com.linkedin.sdui.comments.createComment" {
						t.Fatalf("wrong comment endpoint %s", r.URL)
					}
					postCount++
					data, err := os.ReadFile(filepath.Join(opts.recordDir, hash+".json"))
					if err != nil {
						t.Fatalf("comment sent before attempt was durable: %v", err)
					}
					var attempt commentAttempt
					if json.Unmarshal(data, &attempt) != nil || attempt.Package.Published || attempt.Package.ParentURL != postPermalink {
						t.Fatal("attempt does not bind unconfirmed parent")
					}
					var body struct {
						States []struct {
							Key   string
							Value any
						}
						RequestedArguments struct{ Payload map[string]any }
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					plain, rich := false, false
					for _, state := range body.States {
						if strings.HasPrefix(state.Key, "commentBoxText-") {
							plain = state.Value == text
						}
						if strings.HasPrefix(state.Key, "richCommentBoxText-") {
							rich = state.Value.(map[string]any)["text"] == text
						}
					}
					payload, _ := json.Marshal(body.RequestedArguments.Payload["collection"])
					if !plain || !rich || !strings.Contains(string(payload), `"activityId":"123456789"`) {
						t.Fatal("comment lost approved text or parent")
					}
					if tc.timeout {
						return nil, context.DeadlineExceeded
					}
					resp := jsonResponse([]byte(`0:{"response":{"errors":[],"completionAction":{"actions":[]}}}`))
					if tc.rateLimit {
						resp.StatusCode = 429
					}
					return resp, nil
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
			})}
			result, err := client.createComment(context.Background(), ref, draft, opts)
			wantError := tc.foreignParent || tc.changedApproval || tc.timeout || tc.rateLimit || tc.wrongAuthor || tc.wrongText || tc.wrongParent
			if (err != nil) != wantError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.preview || tc.foreignParent || tc.changedApproval {
				if postCount != 0 {
					t.Fatal("preview or denied package mutated service")
				}
				return
			}
			if postCount != 1 {
				t.Fatalf("unexpected sends %d", postCount)
			}
			duplicate, duplicateErr := client.createComment(context.Background(), ref, draft, opts)
			if postCount != 1 || (duplicateErr != nil) != wantError {
				t.Fatalf("duplicate retry: %d %v", postCount, duplicateErr)
			}
			if !wantError && (!result.Verified || !result.Published || result.Comment.ID != "456" || result.Comment.Text != text || !duplicate.AlreadyPresent) {
				t.Fatalf("comment not independently verified: %+v", result)
			}
		})
	}
}
