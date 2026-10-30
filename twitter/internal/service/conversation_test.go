package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

func conversationTweet(id, author, parent string) string {
	return fmt.Sprintf(`{"__typename":"Tweet","rest_id":%q,"core":{"user_results":{"result":{"__typename":"User","rest_id":%q,"core":{"name":"Example","screen_name":"example%s"}}}},"legacy":{"id_str":%q,"user_id_str":%q,"full_text":"Synthetic post %s","in_reply_to_status_id_str":%q}}`, id, author, author, id, author, id, parent)
}

func conversationBody(posts ...string) string {
	return `{"data":{"threaded_conversation_with_injections_v2":{"instructions":[{"entries":[` + strings.Join(posts, ",") + `]}]}}}`
}

func TestReadConversationIncludesEveryParentAndVisibleDirectReplies(t *testing.T) {
	root := conversationTweet("100", "10", "")
	parent := conversationTweet("101", "11", "100")
	directParent := conversationTweet("102", "12", "101")
	focal := conversationTweet("103", "13", "102")
	reply := conversationTweet("104", "12", "103")
	// A quoted post can claim any reply target; it is still not a conversation entry.
	focal = strings.TrimSuffix(focal, "}") + `,"quoted_status_result":{"result":` + conversationTweet("999", "10", "103") + `}}`
	for _, tc := range []struct {
		name          string
		body          string
		wantParents   []string
		missingParent string
		wantReplies   []string
		wantError     string
	}{
		{
			name:        "three parents and a reply already in the conversation",
			body:        conversationBody(directParent, root, focal, parent, reply, reply, conversationTweet("105", "13", "104"), conversationTweet("106", "10", "100")),
			wantParents: []string{"100", "101", "102"},
			wantReplies: []string{"104"},
		},
		{
			name:          "missing ancestor is reported without another request",
			body:          conversationBody(focal, directParent, reply),
			wantParents:   []string{"102"},
			missingParent: "101",
			wantReplies:   []string{"104"},
		},
		{
			name: "root post has an empty complete parent chain",
			body: conversationBody(conversationTweet("103", "13", "")),
		},
		{
			name:      "cyclic parent links stop within the same response",
			body:      conversationBody(focal, conversationTweet("102", "12", "103")),
			wantError: "parent cycle",
		},
		{
			name:      "a quoted focal post cannot replace a missing conversation entry",
			body:      conversationBody(strings.TrimSuffix(root, "}") + `,"quoted_status_result":{"result":` + focal + `}}`),
			wantError: `requested post "103" is unavailable`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads []string
			client := Client{operations: map[string]string{"TweetDetail": "fixture"}}
			client.http = &http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/TweetDetail") {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var variables struct {
					ID string `json:"focalTweetId"`
				}
				if err := json.Unmarshal([]byte(r.URL.Query().Get("variables")), &variables); err != nil {
					t.Fatal(err)
				}
				reads = append(reads, variables.ID)
				if variables.ID != "103" || len(reads) != 1 {
					t.Fatalf("unexpected conversation read for %q", variables.ID)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
			})}
			ref, err := domain.NewPostRef("103")
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.ReadConversation(context.Background(), ref)
			if len(reads) != 1 {
				t.Fatalf("want one conversation request, got %v", reads)
			}
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("want error containing %q, got %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Post.ID != "103" || got.Post.Author.ID != "13" || got.Post.Text != "Synthetic post 103" {
				t.Fatalf("wrong focal post: %+v", got.Post)
			}
			var parentIDs []string
			for _, post := range got.Parents {
				parentIDs = append(parentIDs, post.ID)
			}
			if !slices.Equal(parentIDs, tc.wantParents) {
				t.Fatalf("want parents %v oldest first, got %v", tc.wantParents, parentIDs)
			}
			var replyIDs []string
			for _, post := range got.Replies {
				replyIDs = append(replyIDs, post.ID)
				if post.Author.ID != "12" || post.Text != "Synthetic post 104" {
					t.Fatalf("wrong visible direct reply: %+v", post)
				}
			}
			if !slices.Equal(replyIDs, tc.wantReplies) {
				t.Fatalf("want replies %v, got %v", tc.wantReplies, replyIDs)
			}
			if got.ParentsComplete != (tc.missingParent == "") || got.MissingParentID != tc.missingParent {
				t.Fatalf("wrong parent coverage: complete=%v missing=%q", got.ParentsComplete, got.MissingParentID)
			}
			if got.Parents == nil || got.Replies == nil {
				t.Fatal("empty parent and reply lists must be JSON arrays")
			}
		})
	}
}
