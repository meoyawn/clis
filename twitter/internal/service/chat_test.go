package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service/transaction"
)

const chatTestID = "12345678-1234-4567-89ab-123456789abc"

func TestPlainChatEventMatchesInspectedBinaryContract(t *testing.T) {
	encoded, err := plainChatEvent("A")
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	const want = "0b0064000000110c00010c00010b00010000000141000000020066010200690008006a0000000100"
	if err != nil || hex.EncodeToString(data) != want {
		t.Fatalf("plain event = %x (%v), want %s", data, err, want)
	}
	for _, text := range []string{"", "\xff", strings.Repeat("x", 10001)} {
		if _, err := plainChatEvent(text); err == nil {
			t.Fatal("accepted invalid message text")
		}
	}
	for _, malformed := range [][]byte{{11, 0, 1, 255, 255, 255, 255}, {0, 1}, {99, 0, 1}, {2, 0, 1, 1, 2, 0, 1, 0, 0}} {
		if _, err := decodeChatStruct(malformed); err == nil {
			t.Fatalf("accepted malformed event %x", malformed)
		}
	}
}

func syntheticChatReceipt(id, sender, conversation, text string) string {
	encoded, _ := plainChatEvent(text)
	create, _ := base64.StdEncoding.DecodeString(encoded)
	detail := append(chatField(12, 1, create), 0)
	event := chatString(1, []byte("900"))
	for _, field := range []struct {
		id    uint16
		value string
	}{{2, id}, {3, sender}, {4, conversation}} {
		event = append(event, chatString(field.id, []byte(field.value))...)
	}
	event = append(event, chatField(12, 7, detail)...)
	return base64.StdEncoding.EncodeToString(append(event, 0))
}

func TestSendChatVerifiesReceiptAndNeverRetries(t *testing.T) {
	for _, name := range []string{"ok", "wrong-id", "wrong-sender", "wrong-conversation", "wrong-text", "429", "denied", "encrypted-recipient", "encrypted-conversation"} {
		t.Run(name, func(t *testing.T) {
			allow, encrypted := true, false
			availability := MessagingAvailability{Recipient: FollowState{Profile: domain.Profile{ID: "99", Handle: "recipient"}}, MessagePermissions: MessagePermissions{CanMessage: &allow, CanDMOnXChat: &encrypted}}
			page := ChatPage{ConversationID: "42:99"}
			if name == "denied" {
				allow = false
			}
			if name == "encrypted-recipient" {
				encrypted = true
			}
			if name == "encrypted-conversation" {
				page.HasKeys = true
			}
			writes := 0
			client := NewClient(&http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
				writes++
				if r.Method != http.MethodPost || r.URL.String() != "https://api.x.com/graphql/test-send/SendMessageCreateMutation" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL)
				}
				var body struct {
					Variables map[string]string `json:"variables"`
					QueryID   string            `json:"queryId"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				wantEvent, _ := plainChatEvent("A")
				if body.QueryID != "test-send" || body.Variables["message_id"] != chatTestID || body.Variables["conversation_id"] != "42:99" || body.Variables["encoded_message_create_event"] != wantEvent || len(body.Variables) != 3 {
					t.Fatalf("wrong send variables: %+v", body)
				}
				id, sender, conv, messageText := chatTestID, "42", "42:99", "A"
				switch name {
				case "wrong-id":
					id = "different"
				case "wrong-sender":
					sender = "99"
				case "wrong-conversation":
					conv = "42:77"
				case "wrong-text":
					messageText = "other"
				}
				status := http.StatusOK
				response := fmt.Sprintf(`{"data":{"xchat_send_create_message_event":{"encoded_message_event":%q}}}`, syntheticChatReceipt(id, sender, conv, messageText))
				if name == "429" {
					status, response = 429, `{"errors":[{"code":88}]}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})}, Session{})
			client.owner = domain.Profile{ID: "42", Handle: "sender"}
			client.chatModule = []byte("this.id=`test-send`,this.name=`SendMessageCreateMutation`")
			var receipt bytes.Buffer
			message, err := client.SendChat(context.Background(), availability, page, chatTestID, "A", &receipt)
			wantWrites := 1
			if name == "denied" || strings.HasPrefix(name, "encrypted-") {
				wantWrites = 0
			}
			if (err == nil) != (name == "ok") || writes != wantWrites {
				t.Fatalf("error=%v writes=%d, want success=%t writes=%d", err, writes, name == "ok", wantWrites)
			}
			if name == "ok" && (message.ID != chatTestID || message.Text != "A" || message.SenderID != "42" || receipt.Len() == 0) {
				t.Fatalf("incorrect confirmed message: %+v", message)
			}
		})
	}
}

func TestReadChatUsesExactRecipientWithoutWrites(t *testing.T) {
	client := NewClient(&http.Client{Transport: timelineTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/graphql/test-read/GetConversationPageQuery" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(r.URL.Query().Get("variables")), &v); err != nil {
			t.Fatal(err)
		}
		if v["conversation_id"] != "42:99" || v["min_local_sequence_id"] != "9223372036854775807" || v["min_conversation_key_version"] != "9223372036854775807" {
			t.Fatalf("wrong page variables: %v", v)
		}
		body := fmt.Sprintf(`{"data":{"get_conversation_page":{"encoded_message_events":[%q],"has_more":false,"missing_conversation_key_change_events":[]}}}`, syntheticChatReceipt(chatTestID, "42", "42:99", "hello 世界\n"))
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, Session{})
	client.owner = domain.Profile{ID: "42"}
	client.chatModule = []byte("this.id=`test-read`,this.name=`GetConversationPageQuery`")
	// ReadChat calls the shared write-metadata preparer even for read-only queries.
	client.transaction = &transaction.Generator{}
	page, err := client.ReadChat(context.Background(), domain.Profile{ID: "99"})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "hello 世界\n" {
		t.Fatalf("page=%+v, error=%v", page, err)
	}
}
