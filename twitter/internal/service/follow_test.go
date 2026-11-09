package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service/transaction"
)

func TestFollowBindsTargetAndVerifiesRelationship(t *testing.T) {
	for _, tc := range []struct {
		name       string
		already    bool
		protected  bool
		blocked    bool
		incomplete bool
		wrongID    bool
		badReceipt bool
		unverified bool
		wantError  bool
		wantPosts  int
	}{
		{name: "public follow", wantPosts: 1},
		{name: "protected request", protected: true, wantPosts: 1},
		{name: "already followed", already: true},
		{name: "blocked relationship", blocked: true, wantError: true},
		{name: "missing state", incomplete: true, wantError: true},
		{name: "changed target", wrongID: true, wantError: true},
		{name: "wrong receipt", badReceipt: true, wantError: true, wantPosts: 1},
		{name: "readback remains unfollowed", unverified: true, wantError: true, wantPosts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, reads := 0, 0
			transport := timelineTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch {
				case r.Method == http.MethodGet && r.URL.String() == "https://x.com/recipient":
					if r.Header.Get("Authorization") != "" || r.Header.Get("X-CSRF-Token") != "" {
						t.Fatal("HTML profile reads must not carry API authorization headers")
					}
					reads++
					following := tc.already || (posts > 0 && !tc.protected && !tc.unverified)
					requested := posts > 0 && tc.protected
					user := fmt.Sprintf(`{"__typename":"User","rest_id":"99","core":{"name":"Recipient","screen_name":"recipient"},"is_following":%t,"follow_request_sent":%t,"privacy":{"protected":%t},"relationship_perspectives":{"following":%t,"blocking":%t,"blocked_by":false}}`, following, requested, tc.protected, following, tc.blocked)
					if tc.incomplete {
						user = strings.ReplaceAll(user, `"blocked_by":false`, `"muting":false`)
					}
					encoded, err := json.Marshal(`{"data":{"user":{"result":` + user + `}}}`)
					if err != nil {
						t.Fatal(err)
					}
					body = `<script>window.__INITIAL_DATA__={"viewerId":"42","viewerScreenName":"example"};</script><script>({kind:"Result.Ok",value:` + string(encoded) + `})</script>`
				case r.Method == http.MethodPost && r.URL.String() == "https://api.x.com/1.1/friendships/create.json":
					posts++
					if err := r.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if reads != 1 || posts != 1 || len(r.PostForm) != 1 || r.PostForm.Get("user_id") != "99" || r.Header.Get("X-CSRF-Token") != "synthetic-csrf" || r.Header.Get("X-Client-Transaction-Id") == "" {
						t.Fatalf("follow lost account, target, signing or single-attempt ordering: %s", r.PostForm.Encode())
					}
					body = `{"id_str":"99"}`
					if tc.badReceipt {
						body = `{"id_str":"77"}`
					}
				default:
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			httpClient := HTTPClient()
			httpClient.Transport = transport
			client := NewClient(httpClient, Session{AuthToken: "synthetic-auth", CSRF: "synthetic-csrf", TWID: "u%3D42"})
			client.owner = domain.Profile{ID: "42", Handle: "example"}
			client.redesigned = true
			client.bearer = "synthetic-public-client"
			client.transaction = &transaction.Generator{}
			target := domain.Profile{ID: "99", Handle: "recipient"}
			if tc.wrongID {
				target.ID = "77"
			}
			var receipt bytes.Buffer
			state, err := client.Follow(context.Background(), target, &receipt)
			if (err != nil) != tc.wantError || posts != tc.wantPosts {
				t.Fatalf("err=%v posts=%d; want error=%t posts=%d", err, posts, tc.wantError, tc.wantPosts)
			}
			if posts > 0 && receipt.Len() == 0 {
				t.Fatal("mutation receipt was not preserved")
			}
			if !tc.wantError && (state.Profile.ID != "99" || (!state.Following && !state.Requested) || (posts > 0 && reads != 2)) {
				t.Fatalf("follow was reported without exact profile readback: %+v, reads=%d", state, reads)
			}
		})
	}
}

func TestClassicFollowStateUsesRelationshipPerspective(t *testing.T) {
	for _, name := range []string{"available", "missing-following", "conflicting-following"} {
		t.Run(name, func(t *testing.T) {
			client := NewClient(&http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/i/api/graphql/test-profile/UserByScreenName" {
					t.Fatalf("unexpected profile request %s %s", r.Method, r.URL)
				}
				user := `{"__typename":"User","rest_id":"99","core":{"name":"Recipient","screen_name":"recipient"},"follow_request_sent":false,"privacy":{"protected":false},"relationship_perspectives":{"following":true,"blocking":false,"blocked_by":false}}`
				if name == "missing-following" {
					user = strings.Replace(user, `"following":true,`, "", 1)
				}
				if name == "conflicting-following" {
					user = strings.Replace(user, `"rest_id":"99"`, `"rest_id":"99","is_following":false`, 1)
				}
				body := `{"data":{"user":{"result":` + user + `}}}`
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}, Session{})
			client.owner = domain.Profile{ID: "42"}
			client.operations = map[string]string{"UserByScreenName": "test-profile"}
			state, err := client.Following(context.Background(), "recipient")
			if (err == nil) != (name == "available") || (err == nil && (!state.Following || state.Requested || state.BlockedBy || state.Blocking)) {
				t.Fatalf("state=%+v error=%v", state, err)
			}
		})
	}
}
