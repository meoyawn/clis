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
)

func TestDeleteExactOwnedUnpublishedArticleDraft(t *testing.T) {
	const id = "123456789"
	const entity = `{"entityUrn":"urn:li:fsd_firstPartyArticle:123456789","title":"Unsent example","contentHtml":"<p>Private draft</p>","state":"DRAFT","version":2,"updatedAt":123,"authors":[{"profileUrn":"urn:li:fsd_profile:ACoSelf"}]}`
	for _, tc := range []struct {
		name                                                                                           string
		preview, published, scheduled, foreignOwner, changedHash, timeout, restliMissing, bareNotFound bool
	}{
		{name: "preview never deletes", preview: true},
		{name: "delete and independent absence read"},
		{name: "RESTli not found confirms absence", restliMissing: true},
		{name: "generic not found does not confirm absence", bareNotFound: true},
		{name: "published article refused", published: true},
		{name: "scheduled article refused", scheduled: true},
		{name: "another author refused", foreignOwner: true},
		{name: "edited draft refused", changedHash: true},
		{name: "uncertain delete blocks retry", timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var draft articleDraft
			if err := json.Unmarshal([]byte(entity), &draft); err != nil {
				t.Fatal(err)
			}
			if tc.published {
				draft.State = "PUBLISHED"
			}
			if tc.scheduled {
				v := int64(12345)
				draft.ScheduledAt = &v
			}
			if tc.foreignOwner {
				draft.Authors[0].ProfileURN = "urn:li:fsd_profile:Foreign"
			}
			encoded, err := json.Marshal(draft)
			if err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
			opts := articleOptions{delete: !tc.preview, author: postAuthorURL, hash: hash, recordDir: filepath.Join(t.TempDir(), "deletions")}
			if tc.changedHash {
				opts.hash = strings.Repeat("0", 64)
			}
			deletes := 0
			c := linkedinClient{session: session{LIAT: "test", JSessionID: "ajax:test"}}
			c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				c.lastRequestAt = time.Time{}
				switch r.URL.Path {
				case "/voyager/api/me":
					return jsonResponse([]byte(`{"data":{"*miniProfile":"urn:li:fs_miniProfile:ACoSelf"}}`)), nil
				case "/voyager/api/identity/dash/profiles":
					return jsonResponse([]byte(`{"data":{"*elements":["urn:li:fsd_profile:ACoSelf"]},"included":[{"entityUrn":"urn:li:fsd_profile:ACoSelf","publicIdentifier":"example-author"}]}`)), nil
				case "/voyager/api/voyagerPublishingDashFirstPartyArticles":
					if r.Method != http.MethodGet || r.URL.Query().Get("q") != "latestDraftById" || r.URL.Query().Get("firstPartyArticleUrn") != draft.URN {
						t.Fatalf("wrong draft read %s", r.URL)
					}
					if deletes > 0 && !tc.timeout {
						if tc.restliMissing || tc.bareNotFound {
							resp := jsonResponse([]byte(`{"status":404}`))
							resp.StatusCode = 404
							if tc.restliMissing {
								resp.Header.Set("X-Restli-Error-Response", "true")
							}
							return resp, nil
						}
						return jsonResponse([]byte(`{"elements":[],"paging":{"start":0,"count":10}}`)), nil
					}
					return jsonResponse([]byte(`{"elements":[` + string(encoded) + `],"paging":{"start":0,"count":10}}`)), nil
				case "/voyager/api/voyagerPublishingDashFirstPartyArticles/urn:li:fsd_firstPartyArticle:123456789":
					if r.Method != http.MethodDelete {
						t.Fatalf("unexpected mutation %s", r.Method)
					}
					deletes++
					backup := filepath.Join(opts.recordDir, id+"-"+hash+".json")
					data, err := os.ReadFile(backup)
					if err != nil || !strings.Contains(string(data), "Private draft") {
						t.Fatalf("draft deleted before private backup: %v", err)
					}
					info, err := os.Stat(backup)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("draft backup is not private")
					}
					if tc.timeout {
						return nil, context.DeadlineExceeded
					}
					resp := jsonResponse(nil)
					resp.StatusCode = http.StatusNoContent
					return resp, nil
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
			})}
			result, err := c.articleDraftAction(context.Background(), id, opts)
			wantError := tc.published || tc.scheduled || tc.foreignOwner || tc.changedHash || tc.timeout || tc.bareNotFound
			if (err != nil) != wantError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if tc.preview || tc.published || tc.scheduled || tc.foreignOwner || tc.changedHash {
				if deletes != 0 {
					t.Fatal("unapproved or published article deleted")
				}
				return
			}
			if deletes != 1 {
				t.Fatalf("DELETE count=%d", deletes)
			}
			if !tc.timeout && !tc.bareNotFound && (!result.Deleted || !result.Verified || result.Exists) {
				t.Fatalf("absence not verified: %+v", result)
			}
			again, err := c.articleDraftAction(context.Background(), id, opts)
			if deletes != 1 || (err != nil) != (tc.timeout || tc.bareNotFound) {
				t.Fatalf("second invocation repeated DELETE or lost guard: %+v %v", again, err)
			}
		})
	}
}
