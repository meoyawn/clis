package service

import (
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

func TestMessagingAvailabilityUsesExactChatPermissionsWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions string
		want        string
		wrongID     bool
		foreignCDN  bool
		rateLimited bool
		wantError   bool
	}{
		{name: "available without encrypted Chat", permissions: `{"can_dm":true,"can_dm_on_xchat":false,"can_dm_reason":"Allowed","dm_blocking":false,"passes_premium_check":true}`, want: "true"},
		{name: "closed inbox", permissions: `{"can_dm":false,"can_dm_on_xchat":false,"can_dm_reason":"InboxClosed","dm_blocking":false,"passes_premium_check":true}`, want: "false"},
		{name: "premium restriction overrides allowed", permissions: `{"can_dm":true,"can_dm_on_xchat":true,"can_dm_reason":"Allowed","dm_blocking":false,"passes_premium_check":false}`, want: "false"},
		{name: "missing permissions stay unknown", permissions: `null`, want: "null"},
		{name: "missing recipient checks stay unknown", permissions: `{"can_dm":true,"can_dm_on_xchat":true}`, want: "null"},
		{name: "unfamiliar reason stays unknown", permissions: `{"can_dm":true,"can_dm_on_xchat":true,"can_dm_reason":"NewReason","dm_blocking":false,"passes_premium_check":true}`, want: "null"},
		{name: "different recipient is rejected", permissions: `{"can_dm_on_xchat":true}`, wrongID: true, wantError: true},
		{name: "foreign public module is refused", foreignCDN: true, wantError: true},
		{name: "rate limit is not retried", rateLimited: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiReads := 0
			transport := timelineTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatalf("permission check attempted %s %s", r.Method, r.URL)
				}
				if (r.URL.Host == "chat.x.com" || r.URL.Host == "abs.twimg.com") && (r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-CSRF-Token") != "") {
					t.Fatal("session credentials leaked to public Chat metadata")
				}
				body := ""
				status := http.StatusOK
				switch r.URL.Host + r.URL.Path {
				case "x.com/recipient":
					const user = `{"__typename":"User","rest_id":"99","core":{"name":"Recipient","screen_name":"recipient"},"is_following":true,"follow_request_sent":false,"privacy":{"protected":false},"relationship_perspectives":{"following":true,"blocking":false,"blocked_by":false}}`
					encoded, err := json.Marshal(`{"data":{"user":{"result":` + user + `}}}`)
					if err != nil {
						t.Fatal(err)
					}
					body = `<script>window.__INITIAL_DATA__={"viewerId":"42","viewerScreenName":"example"};</script><script>({kind:"Result.Ok",value:` + string(encoded) + `})</script>`
				case "chat.x.com/embed-v1.json":
					body = `{"metaData":{"publicPath":"https://abs.twimg.com/x-web/xchat/embed/"},"exposes":[{"assets":{"js":{"async":["assets/dm-core-example.js"]}}}]}`
					if tc.foreignCDN {
						body = strings.ReplaceAll(body, "abs.twimg.com", "example.net")
					}
				case "abs.twimg.com/x-web/xchat/embed/assets/dm-core-example.js":
					body = "this.queryId=`synthetic-query`,this.name=`GetUsersByIdsForXChat`"
				case "api.x.com/graphql/synthetic-query/GetUsersByIdsForXChat":
					apiReads++
					var variables struct {
						IDs   []string `json:"ids"`
						Proof bool     `json:"with_social_proof"`
					}
					if err := json.Unmarshal([]byte(r.URL.Query().Get("variables")), &variables); err != nil {
						t.Fatal(err)
					}
					if len(variables.IDs) != 1 || variables.IDs[0] != "99" || variables.Proof || r.Header.Get("X-Client-Transaction-Id") == "" || r.Header.Get("X-CSRF-Token") != "synthetic-csrf" {
						t.Fatal("permission query lost the exact recipient or selected account")
					}
					id := "99"
					if tc.wrongID {
						id = "77"
					}
					body = fmt.Sprintf(`{"data":{"get_member_results":{"results":[{"id":"99","status":"Found","member_results":{"rest_id":"99","result":{"__typename":"User","rest_id":%q,"core":{"name":"Recipient","screen_name":"recipient"},"chat_permissions":%s}}}]}}}`, id, tc.permissions)
					if tc.rateLimited {
						status, body = http.StatusTooManyRequests, `{"errors":[{"code":88}]}`
					}
				default:
					t.Fatalf("unexpected permission request %s", r.URL)
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			httpClient := HTTPClient()
			httpClient.Transport = transport
			client := NewClient(httpClient, Session{AuthToken: "synthetic-auth", CSRF: "synthetic-csrf", TWID: "u%3D42"})
			client.owner = domain.Profile{ID: "42", Handle: "example"}
			client.redesigned = true
			client.bearer = "synthetic-public-client"
			client.transaction = &transaction.Generator{}
			result, err := client.MessagingAvailability(context.Background(), "recipient")
			if (err != nil) != tc.wantError || apiReads > 1 {
				t.Fatalf("err=%v, api reads=%d; want error=%t without retries", err, apiReads, tc.wantError)
			}
			if !tc.wantError {
				permission, err := json.Marshal(result.CanMessage)
				if err != nil || string(permission) != tc.want || result.Recipient.Profile.ID != "99" || !result.Recipient.Following {
					t.Fatalf("incorrect recipient or Chat permission: %+v (%v)", result, err)
				}
			}
		})
	}
}
