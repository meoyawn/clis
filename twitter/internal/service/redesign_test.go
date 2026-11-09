package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

func TestRedesignedSessionReadsConversation(t *testing.T) {
	const user = `{"__typename":"User","rest_id":"42","core":{"name":"Example Author","screen_name":"example"},"relationship_counts":{"followers":12}}`
	const focal = `{"__typename":"Tweet","rest_id":"123","core":{"user_results":{"result":` + user + `}},"details":{"full_text":"Wake the agent https://t.co/article https://t.co/photo","created_at_ms":1000},"counts":{"favorite_count":7,"reply_count":0},"url_entities":[{"url":"https://t.co/article","expanded_url":"https://example.com/ci"}],"media_entities2":[{"id_str":"456","type":"photo","indices":[36,54],"media_url_https":"https://pbs.twimg.com/media/example.png"}],"views":{"count":"0"}}`
	const reply = `{"__typename":"Tweet","rest_id":"124","core":{"user_results":{"result":` + user + `}},"details":{"full_text":"Try again","created_at_ms":2000},"reply_to_results":{"__typename":"TweetResults","rest_id":"123"}}`
	for _, tc := range []struct {
		name         string
		wrongAccount bool
		redirect     string
		denyRedirect bool
	}{
		{name: "current page"},
		{name: "different account", wrongAccount: true},
		{name: "canonical post redirect", redirect: "/example/status/123"},
		{name: "foreign redirect is refused", redirect: "https://example.net/example/status/123", denyRedirect: true},
		{name: "different post redirect is refused", redirect: "/example/status/999", denyRedirect: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := `<script>window.__INITIAL_DATA__={"viewerId":"42","viewerScreenName":"example"};</script>`
			document := func(data string) string {
				encoded, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				return initial + `<script>($R=>$R[1]={kind:"Result.Ok",value:` + string(encoded) + `})($R["tsr"]);</script>`
			}
			postReads := 0
			transport := timelineTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Host != "x.com" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				if !strings.Contains(r.Header.Get("Cookie"), "auth_token=synthetic-auth") {
					t.Fatal("account request lost the selected session")
				}
				var body string
				switch r.URL.Path {
				case "/home":
					body = document(`{"data":{"viewer":{"user_results":{"result":` + user + `}}}}`)
				case "/example/status/123":
					postReads++
					body = document(`{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"entries":[{"content":{"itemContent":{"tweet_results":{"result":` + focal + `}}}},{"content":{"itemContent":{"tweet_results":{"result":` + reply + `}}}}]}]},"tweet_result_by_rest_id":{"result":` + focal + `}}}`)
				case "/i/status/123":
					postReads++
					return &http.Response{StatusCode: 307, Header: http.Header{"Location": {tc.redirect}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				default:
					t.Fatalf("unexpected page %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			twid := "u%3D42"
			if tc.wrongAccount {
				twid = "u%3D99"
			}
			httpClient := HTTPClient()
			httpClient.Transport = transport
			client := NewClient(httpClient, Session{AuthToken: "synthetic-auth", CSRF: "synthetic-csrf", TWID: twid})
			owner, err := client.Bootstrap(context.Background())
			if tc.wrongAccount {
				if err == nil || postReads != 0 {
					t.Fatal("a different account was accepted or used for reading")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if owner.ID != "42" || owner.Handle != "example" || owner.Followers != 12 {
				t.Fatalf("wrong selected profile: %+v", owner)
			}
			postURL := "https://x.com/example/status/123"
			wantReads := 1
			if tc.redirect != "" {
				postURL = "https://x.com/i/status/123"
				wantReads = 2
			}
			ref, err := domain.NewPostRef(postURL)
			if err != nil {
				t.Fatal(err)
			}
			conversation, err := client.ReadConversation(context.Background(), ref)
			if tc.denyRedirect {
				if err == nil || postReads != 1 {
					t.Fatal("an unrelated redirect was followed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			post := conversation.Post
			if postReads != wantReads || post.ID != "123" || post.Author.ID != owner.ID || post.Text != "Wake the agent https://example.com/ci" || post.CreatedAt != "Thu Jan 01 00:00:01 +0000 1970" || post.Likes != 7 || post.Views == nil || *post.Views != 0 {
				t.Fatalf("redesigned page changed the post or its counters: %+v", post)
			}
			if len(post.MediaIDs) != 1 || post.MediaIDs[0] != "456" || len(post.ImageURLs) != 1 || post.ImageURLs[0] != "https://pbs.twimg.com/media/example.png" || !conversation.ParentsComplete || len(conversation.Parents) != 0 || len(conversation.Replies) != 1 || conversation.Replies[0].ReplyTo != post.ID {
				t.Fatalf("redesigned conversation lost media or invented relatives: %+v", conversation)
			}
		})
	}
}
