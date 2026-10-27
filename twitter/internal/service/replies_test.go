package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

func TestOwnRepliesChecksParentAuthorAndBothReadSources(t *testing.T) {
	makeTweet := func(id, author, parent string) string {
		reply := ""
		if parent != "" {
			reply = `,"in_reply_to_status_id_str":"` + parent + `"`
		}
		return fmt.Sprintf(`{"__typename":"Tweet","rest_id":"%s","core":{"user_results":{"result":{"__typename":"User","rest_id":"%s","core":{"name":"Example","screen_name":"example"}}}},"legacy":{"id_str":"%s","user_id_str":"%s","full_text":"Example"%s}}`, id, author, id, author, reply)
	}
	envelope := func(posts ...string) string { return `{"data":{"items":[` + strings.Join(posts, ",") + `]}}` }
	for _, tc := range []struct {
		name, parentAuthor string
		missing            bool
		wantError          string
	}{
		{name: "merge exact own replies from both sources", parentAuthor: "42"},
		{name: "foreign parent", parentAuthor: "99", wantError: "does not belong"},
		{name: "parent unavailable", missing: true, wantError: "confirm parent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			client := Client{owner: domain.Profile{ID: "42", Handle: "example"}, operations: map[string]string{"TweetDetail": "fixture", "UserRepliesTimeline": "fixture"}}
			client.http = &http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("read performed a mutation")
				}
				reads++
				body := ""
				switch {
				case strings.HasSuffix(r.URL.Path, "/TweetDetail"):
					if tc.missing {
						body = envelope(makeTweet("999", "42", ""))
					} else {
						body = envelope(makeTweet("100", tc.parentAuthor, ""), makeTweet("101", "42", "100"), makeTweet("102", "99", "100"), makeTweet("103", "42", "999"))
					}
				case strings.HasSuffix(r.URL.Path, "/UserRepliesTimeline"):
					body = envelope(makeTweet("101", "42", "100"), makeTweet("104", "42", "100"), makeTweet("105", "42", "999"))
				default:
					t.Fatalf("unexpected endpoint %s", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}
			ref, err := domain.NewPostRef("100")
			if err != nil {
				t.Fatal(err)
			}
			posts, err := client.OwnReplies(context.Background(), ref)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || reads != 1 {
					t.Fatalf("unverified parent was followed: reads=%d err=%v", reads, err)
				}
				return
			}
			if err != nil || len(posts) != 2 || reads != 2 {
				t.Fatalf("replies=%+v reads=%d err=%v", posts, reads, err)
			}
			ids := map[string]bool{}
			for _, p := range posts {
				ids[p.ID] = true
				if p.ReplyTo != "100" || p.Author.ID != "42" {
					t.Fatalf("wrong reply destination or identity: %+v", p)
				}
			}
			if !ids["101"] || !ids["104"] {
				t.Fatalf("lost verified replies: %+v", posts)
			}
		})
	}
}
