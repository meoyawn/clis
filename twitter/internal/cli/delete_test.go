package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

func TestDeletionLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		wrongReceipt bool
		timeout      bool
		otherAuthor  bool
	}{
		{name: "matching deletion receipt"},
		{name: "different deleted ID is unconfirmed", wrongReceipt: true},
		{name: "timeout blocks another mutation", timeout: true},
		{name: "another author cannot be deleted", otherAuthor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			posts := 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "abs.twimg.com" {
					if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
						t.Fatal("account secrets reached a public module")
					}
					switch {
					case strings.HasSuffix(r.URL.Path, "/entry-client-fixture.js"):
						return response(200, `import{a}from"./assets/shared.js";import{b}from"./assets/routes.js";`), nil
					case strings.HasSuffix(r.URL.Path, "/shared.js"):
						return response(200, `const token="AAAAA`+strings.Repeat("a", 70)+`";`), nil
					case strings.HasSuffix(r.URL.Path, "/routes.js"):
						return response(200, "const route={path:`/compose/post`,lazyComponent:()=>import(`./composer.js`)};const signer=()=>import(`./signer.js`).then(e=>e.default());const author=()=>load(()=>import(`./author.js`),__vite__mapDeps([4])),`AuthorItems`;"), nil
					case strings.HasSuffix(r.URL.Path, "/composer.js"):
						return response(200, "const op={params:{id:`create-query`,metadata:{},name:`CreateTweet`,operationKind:`mutation`,text:null,providedVariables:{__relay_internal__pv__xclientsfeaturesappviewerisloggedinrelayprovider:P}}};"), nil
					case strings.HasSuffix(r.URL.Path, "/signer.js"):
						return response(200, `(a[1],16);(a[2],16);`), nil
					case strings.HasSuffix(r.URL.Path, "/author.js"):
						return response(200, "const op={params:{id:`delete-query`,metadata:{},name:`deleteTweetMutation`,operationKind:`mutation`,text:null}};"), nil
					default:
						t.Fatalf("unexpected module %s", r.URL.Path)
					}
				}
				if r.Method == http.MethodGet && r.URL.Host == "x.com" && r.URL.Path == "/home" {
					encoded, err := json.Marshal(`{"data":{"viewer":{"user_results":{"result":` + userJSON + `}}}}`)
					if err != nil {
						t.Fatal(err)
					}
					return response(200, signingDocument()+`<script>window.__INITIAL_DATA__={"viewerId":"42","viewerScreenName":"example"};</script><script>const entryHref="https://abs.twimg.com/x-web/x-web/entry-client-fixture.js";const result={kind:"Result.Ok",value:`+string(encoded)+`};</script>`), nil
				}
				if r.Method != http.MethodPost || r.URL.Host != "api.x.com" || r.URL.Path != "/graphql/delete-query/deleteTweetMutation" || r.Header.Get("X-Client-Transaction-Id") == "" {
					t.Fatalf("unexpected deletion request %s %s", r.Method, r.URL)
				}
				var body struct {
					Variables struct {
						ID string `json:"tweetId"`
					} `json:"variables"`
					QueryID string `json:"queryId"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Variables.ID != "123" || body.QueryID != "delete-query" {
					t.Fatal("deletion changed the reviewed target or mutation")
				}
				data, err := os.ReadFile(filepath.Join(dir, "deletions", "123.json"))
				if err != nil {
					t.Fatal("deletion was sent before its durable attempt")
				}
				var attempt deletionPreview
				if err := json.Unmarshal(data, &attempt); err != nil || attempt.Deleted || attempt.Post.ID != "123" {
					t.Fatal("deletion did not preserve the reviewed pre-deletion state")
				}
				posts++
				if tc.timeout {
					return nil, context.DeadlineExceeded
				}
				id := "123"
				if tc.wrongReceipt {
					id = "999"
				}
				return response(200, `{"data":{"delete_tweet":{"tweet_results":{"rest_id":"`+id+`"}}}}`), nil
			})
			client := service.NewClient(&http.Client{Transport: transport}, service.Session{AuthToken: "synthetic-auth", CSRF: "synthetic-csrf", TWID: "u%3D42"})
			owner, err := client.Bootstrap(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			post := domain.Post{ID: "123", Author: owner, Text: "Replace this picture"}
			if tc.otherAuthor {
				post.Author.ID = "99"
			}
			result := deletionFor(owner, post)
			var out bytes.Buffer
			err = deletePackage(context.Background(), client, dir, result, &out)
			wantError := tc.timeout || tc.wrongReceipt || tc.otherAuthor
			if (err != nil) != wantError || (tc.otherAuthor && posts != 0) || (!tc.otherAuthor && posts != 1) {
				t.Fatalf("deletion requests=%d error=%v", posts, err)
			}
			if !wantError {
				var deleted deletionPreview
				if err := json.Unmarshal(out.Bytes(), &deleted); err != nil || !deleted.Deleted || !deleted.Verified || deleted.Post.ID != "123" {
					t.Fatal("a matching provider receipt was not recorded")
				}
			}
			before := posts
			if err := deletePackage(context.Background(), client, dir, result, &out); err == nil || posts != before {
				t.Fatal("a second deletion attempt was allowed")
			}
		})
	}
}
