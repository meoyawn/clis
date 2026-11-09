package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/meoyawn/clis/twitter/internal/domain"
)

type ChatPage struct {
	ConversationID    string        `json:"conversation_id"`
	Messages          []ChatMessage `json:"messages"`
	HasMore           bool          `json:"has_more"`
	HasKeys           bool          `json:"has_keys"`
	conversationToken string
}

func chatConversationID(owner, recipient string) (string, error) {
	a, errA := strconv.ParseUint(owner, 10, 64)
	b, errB := strconv.ParseUint(recipient, 10, 64)
	if errA != nil || errB != nil || a == 0 || b == 0 || a == b {
		return "", fmt.Errorf("Chat requires two different numeric user IDs")
	}
	if a > b {
		owner, recipient = recipient, owner
	}
	return owner + ":" + recipient, nil
}

func (c *Client) chatRead(ctx context.Context, operation domain.Operation, variables any) ([]byte, error) {
	id, err := c.chatOperationID(ctx, operation)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(variables)
	if err != nil {
		return nil, err
	}
	query := url.Values{"variables": {string(encoded)}}
	data, err := c.request(ctx, http.MethodGet, "https://api.x.com/graphql/"+id+"/"+operation.String()+"?"+query.Encode(), "", nil, http.Header{"Apollo-Require-Preflight": {"true"}})
	if err != nil {
		return nil, err
	}
	return data, graphQLError(data)
}

// ReadChat reads one exact conversation page without read receipts or keys.
func (c *Client) ReadChat(ctx context.Context, recipient domain.Profile) (ChatPage, error) {
	convID, err := chatConversationID(c.owner.ID, recipient.ID)
	if err != nil {
		return ChatPage{}, err
	}
	if err := c.PrepareWriteMetadata(ctx); err != nil {
		return ChatPage{}, err
	}
	data, err := c.chatRead(ctx, domain.ChatReadOperation(), map[string]any{
		"conversation_id": convID, "min_local_sequence_id": "9223372036854775807", "min_conversation_key_version": "9223372036854775807",
		"query_settings": map[string]int{"conversation_event_limit": 50},
	})
	if err != nil {
		return ChatPage{}, err
	}
	var response struct {
		Data struct {
			Page *struct {
				Events  []string `json:"encoded_message_events"`
				Keys    []string `json:"missing_conversation_key_change_events"`
				HasMore bool     `json:"has_more"`
			} `json:"get_conversation_page"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatPage{}, err
	}
	if response.Data.Page == nil {
		return ChatPage{}, fmt.Errorf("X did not return the exact conversation page")
	}
	p := response.Data.Page
	page := ChatPage{ConversationID: convID, Messages: []ChatMessage{}, HasMore: p.HasMore, HasKeys: len(p.Keys) != 0}
	for _, encoded := range p.Events {
		message, isMessage, err := decodeChatMessage(encoded)
		if err != nil {
			return ChatPage{}, err
		}
		if !isMessage {
			page.HasKeys = page.HasKeys || message.Encrypted
			continue
		}
		if message.ConversationID != convID || (message.SenderID != c.owner.ID && message.SenderID != recipient.ID) {
			return ChatPage{}, fmt.Errorf("X returned a message from a different conversation")
		}
		page.HasKeys = page.HasKeys || message.Encrypted
		if page.conversationToken == "" {
			page.conversationToken = message.conversationToken
		}
		page.Messages = append(page.Messages, message)
	}
	return page, nil
}

var chatMessageID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// SendChat sends exactly once. A plain-only command must never downgrade an
// encrypted conversation. The caller saves its attempt before invoking this.
func (c *Client) SendChat(ctx context.Context, availability MessagingAvailability, page ChatPage, messageID, text string, receipt io.Writer) (ChatMessage, error) {
	convID, err := chatConversationID(c.owner.ID, availability.Recipient.Profile.ID)
	if err != nil {
		return ChatMessage{}, err
	}
	if availability.CanMessage == nil || !*availability.CanMessage || availability.CanDMOnXChat == nil || *availability.CanDMOnXChat || availability.Recipient.Blocking || availability.Recipient.BlockedBy {
		return ChatMessage{}, fmt.Errorf("recipient does not explicitly allow ordinary unencrypted DMs")
	}
	if page.ConversationID != convID || page.HasKeys || !chatMessageID.MatchString(messageID) || receipt == nil {
		return ChatMessage{}, fmt.Errorf("invalid or encrypted conversation, message ID, or receipt destination")
	}
	encoded, err := plainChatEvent(text)
	if err != nil {
		return ChatMessage{}, err
	}
	queryID, err := c.chatOperationID(ctx, domain.ChatSendOperation())
	if err != nil {
		return ChatMessage{}, err
	}
	variables := map[string]any{"conversation_id": convID, "message_id": messageID, "encoded_message_create_event": encoded}
	if page.conversationToken != "" {
		variables["conversation_token"] = page.conversationToken
	}
	body, err := json.Marshal(map[string]any{"variables": variables, "queryId": queryID})
	if err != nil {
		return ChatMessage{}, err
	}
	data, requestErr := c.request(ctx, http.MethodPost, "https://api.x.com/graphql/"+queryID+"/SendMessageCreateMutation", "application/json", body, http.Header{"Apollo-Require-Preflight": {"true"}})
	if _, err := receipt.Write(data); err != nil {
		return ChatMessage{}, fmt.Errorf("save Chat receipt after attempted send: %w", err)
	}
	if requestErr != nil {
		return ChatMessage{}, requestErr
	}
	if err := graphQLError(data); err != nil {
		return ChatMessage{}, err
	}
	var response struct {
		Data struct {
			Send struct {
				Event string `json:"encoded_message_event"`
			} `json:"xchat_send_create_message_event"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return ChatMessage{}, err
	}
	message, isMessage, err := decodeChatMessage(response.Data.Send.Event)
	if err != nil {
		return ChatMessage{}, err
	}
	if !isMessage || message.Encrypted || message.ID != messageID || message.SenderID != c.owner.ID || message.ConversationID != convID || message.Text != text {
		return ChatMessage{}, fmt.Errorf("Chat receipt did not confirm the exact sender, recipient, message ID and text; do not resend")
	}
	return message, nil
}
