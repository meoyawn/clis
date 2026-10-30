package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

type timelineTransport func(*http.Request) (*http.Response, error)

func (f timelineTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The live originals endpoint injects connected replies beyond its requested
// count. Neither those replies nor a quoted self-post belongs in this export.
func TestOwnPostsExcludesInjectedRepliesAndQuotedPosts(t *testing.T) {
	var items []string
	for i := 0; i < 25; i++ {
		reply := ""
		if i < 4 {
			reply = `,"in_reply_to_status_id_str":"10"`
		}
		post := fmt.Sprintf(`{"__typename":"Tweet","rest_id":"%d","core":{"user_results":{"result":{"__typename":"User","rest_id":"42","core":{"name":"Example","screen_name":"example"}}}},"legacy":{"id_str":"%d","user_id_str":"42","full_text":"Example post"%s}}`, 100+i, 100+i, reply)
		if i == 4 {
			quoted := strings.ReplaceAll(post, `"104"`, `"999"`)
			post = strings.TrimSuffix(post, "}") + `,"quoted_status_result":{"result":` + quoted + `}}`
		}
		items = append(items, `{"content":{"itemContent":{"tweet_results":{"result":`+post+`}}}}`)
	}
	body := `{"data":{"user":{"result":{"timeline":{"instructions":[{"entries":[` + strings.Join(items, ",") + `]}]}}}}}`
	c := Client{http: &http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/UserOriginalsTimeline") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}, owner: domain.Profile{ID: "42", Handle: "example"}, operations: map[string]string{"UserOriginalsTimeline": "fixture"}}
	posts, err := c.OwnPosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, post := range posts {
		if post.ReplyTo != "" || post.ID == "999" {
			t.Fatalf("exported injected reply or quoted self-post: %+v", post)
		}
	}
	if len(posts) != 20 {
		t.Fatalf("expected 20 original posts, got %d", len(posts))
	}
}

func TestReadPostExposesFocalPhotoURLs(t *testing.T) {
	const body = `{"data":{"items":[{"__typename":"Tweet","rest_id":"100","core":{"user_results":{"result":{"__typename":"User","rest_id":"42","core":{"name":"Example","screen_name":"example"}}}},"legacy":{"id_str":"100","user_id_str":"42","full_text":"Photos","extended_entities":{"media":[{"id_str":"201","type":"photo","media_url_https":"https://pbs.twimg.com/media/first.jpg"},{"id_str":"202","type":"video","media_url_https":"https://pbs.twimg.com/media/poster.jpg"},{"id_str":"203","type":"photo","media_url_https":"https://pbs.twimg.com/media/second.png"},{"id_str":"204","type":"photo"}]}},"quoted_status_result":{"result":{"__typename":"Tweet","rest_id":"999","legacy":{"extended_entities":{"media":[{"id_str":"205","type":"photo","media_url_https":"https://pbs.twimg.com/media/quoted.jpg"}]}}}}}]}}`
	c := Client{http: &http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/TweetDetail") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}, operations: map[string]string{"TweetDetail": "fixture"}}
	ref, err := domain.NewPostRef("100")
	if err != nil {
		t.Fatal(err)
	}
	post, err := c.ReadPost(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(post.ImageURLs, []string{"https://pbs.twimg.com/media/first.jpg", "https://pbs.twimg.com/media/second.png"}) {
		t.Fatalf("wrong photo URLs: %v", post.ImageURLs)
	}
	if !slices.Equal(post.MediaIDs, []string{"201", "202", "203", "204"}) {
		t.Fatalf("media IDs changed: %v", post.MediaIDs)
	}
}
