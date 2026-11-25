package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/meoyawn/clis/hh/internal/domain"
)

// ReplyApplicationMessage previews a normal message or sends it once after
// approval. The native send contract is published in Chatik's composer bundle;
// the response ID must also appear in a fresh, independently bound chat read.
func (c *Client) ReplyApplicationMessage(ctx context.Context, status *ApplicationStatus, text string, send bool, approvedSHA, backupDir string) (Object, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("reply text is not valid UTF-8; no message sent")
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil, fmt.Errorf("reply text must be nonempty; no message sent")
	}
	if err := c.ReadApplicationMessages(ctx, status); err != nil {
		return nil, err
	}
	if status.Messages["write_allowed"] != true {
		return nil, fmt.Errorf("chat %s does not permit writing; no message sent", status.ChatID)
	}
	before := status.Messages["items"].([]Object)
	lastID := int64(0)
	for _, message := range before {
		id, err := strconv.ParseInt(nativeID(message["id"]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid chat message ID %q: %w", nativeID(message["id"]), err)
		}
		if id > lastID {
			lastID = id
		}
	}
	review := Object{"vacancy_id": status.VacancyID, "vacancy_url": status.VacancyURL, "title": status.Title,
		"employer": status.Employer, "topic_id": status.TopicID, "chat_id": status.ChatID,
		"resume_id": status.ResumeID, "resume_url": status.ResumeURL, "applicant_user_id": status.ApplicantID,
		"after_message_id": strconv.FormatInt(lastID, 10), "text": text}
	encoded, err := json.Marshal(review)
	if err != nil {
		return nil, fmt.Errorf("encode reply review: %w", err)
	}
	digest := sha256.Sum256(encoded)
	review["review_sha256"] = hex.EncodeToString(digest[:])
	review["sent"] = false
	if !send {
		return review, nil
	}
	if approvedSHA == "" || approvedSHA != review["review_sha256"] {
		return nil, fmt.Errorf("reply approval does not match the current destination, text, resume and last message; review again; no message sent")
	}
	if backupDir == "" {
		return nil, fmt.Errorf("reply backup directory is empty; no message sent")
	}
	inspect := "inspect application get --messages before retrying"
	messageID, backupPath, err := c.postChatMessage(ctx, status.ChatID, "reply-"+status.TopicID, text, lastID, review, status.Messages, backupDir, inspect)
	if err != nil {
		return nil, err
	}
	if err := c.ReadApplicationMessages(ctx, status); err != nil {
		return nil, fmt.Errorf("reply returned message %s but chat verification failed: %w; do not resend; backup %s", messageID, err, backupPath)
	}
	for _, message := range status.Messages["items"].([]Object) {
		if nativeID(message["id"]) == messageID && message["from_applicant"] == true && message["text"] == text {
			review["sent"], review["verified"] = true, true
			review["message_id"], review["backup_path"] = messageID, backupPath
			return review, nil
		}
	}
	return nil, fmt.Errorf("reply message %s text or author not confirmed by chat readback; do not resend; backup %s", messageID, backupPath)
}

// ReplyChat previews a normal message in an explicitly selected chat, such as
// a direct recruiter conversation with no application, or sends it once after
// approval of the exact destination, participants, last visible message and text.
func (c *Client) ReplyChat(ctx context.Context, id domain.ChatID, text string, send bool, approvedSHA, backupDir string) (Object, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("reply text is not valid UTF-8; no message sent")
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return nil, fmt.Errorf("reply text must be nonempty; no message sent")
	}
	page, err := c.ReadChat(ctx, id)
	if err != nil {
		return nil, err
	}
	if !page.WriteAllowed {
		return nil, fmt.Errorf("chat %s does not permit writing; no message sent", page.ChatID)
	}
	others := []string{}
	seen := map[string]bool{}
	for _, message := range page.Messages {
		if !message.FromSelf && message.SenderName != "" && !seen[message.SenderName] {
			seen[message.SenderName] = true
			others = append(others, message.SenderName)
		}
	}
	lastID := int64(page.LastMessageID)
	review := Object{"chat_id": page.ChatID, "chat_url": "https://hh.ru/chat/" + page.ChatID, "participant_id": page.ParticipantID,
		"recipients": others, "after_message_id": strconv.FormatInt(lastID, 10), "text": text}
	encoded, err := json.Marshal(review)
	if err != nil {
		return nil, fmt.Errorf("encode reply review: %w", err)
	}
	digest := sha256.Sum256(encoded)
	review["review_sha256"] = hex.EncodeToString(digest[:])
	review["sent"] = false
	if !send {
		return review, nil
	}
	if approvedSHA == "" || approvedSHA != review["review_sha256"] {
		return nil, fmt.Errorf("reply approval does not match the current chat, text and last message; review again; no message sent")
	}
	inspect := "inspect chat read before retrying"
	messageID, backupPath, err := c.postChatMessage(ctx, page.ChatID, "chat-"+page.ChatID, text, lastID, review, page, backupDir, inspect)
	if err != nil {
		return nil, err
	}
	after, err := c.ReadChat(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reply returned message %s but chat verification failed: %w; do not resend; backup %s", messageID, err, backupPath)
	}
	for _, message := range after.Messages {
		if message.ID == messageID && message.FromSelf && message.Text == text {
			review["sent"], review["verified"] = true, true
			review["message_id"], review["backup_path"] = messageID, backupPath
			return review, nil
		}
	}
	return nil, fmt.Errorf("reply message %s text or author not confirmed by chat readback; do not resend; backup %s", messageID, backupPath)
}

// postChatMessage saves a private pre-send backup and posts one plain-text
// message with a fresh idempotency key. It never retries; callers verify the
// returned message by an independent chat read.
func (c *Client) postChatMessage(ctx context.Context, chatText, label, text string, lastID int64, review Object, messages any, backupDir, inspect string) (string, string, error) {
	if backupDir == "" {
		return "", "", fmt.Errorf("reply backup directory is empty; no message sent")
	}
	chatID, err := strconv.ParseInt(chatText, 10, 64)
	if err != nil {
		return "", "", fmt.Errorf("invalid chat ID %q: %w", chatText, err)
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", "", fmt.Errorf("generate message idempotency key: %w", err)
	}
	key[6], key[8] = key[6]&0x0f|0x40, key[8]&0x3f|0x80
	idempotencyKey := fmt.Sprintf("%x-%x-%x-%x-%x", key[:4], key[4:6], key[6:8], key[8:10], key[10:])
	payload := Object{"chatId": chatID, "idempotencyKey": idempotencyKey, "text": text}
	snapshot, err := json.MarshalIndent(Object{"review": review, "request": payload, "messages": messages}, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode reply backup: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return "", "", fmt.Errorf("create reply backup directory %q: %w", backupDir, err)
	}
	backup, err := os.CreateTemp(backupDir, label+"-*.json")
	if err != nil {
		return "", "", fmt.Errorf("create reply backup: %w", err)
	}
	_, writeErr := backup.Write(snapshot)
	closeErr := backup.Close()
	if writeErr != nil {
		return "", "", fmt.Errorf("write reply backup %q: %w", backup.Name(), writeErr)
	}
	if closeErr != nil {
		return "", "", fmt.Errorf("close reply backup %q: %w", backup.Name(), closeErr)
	}
	query := url.Values{"hhtmSourceLabel": {"chat"}}
	body, err := c.request(ctx, http.MethodPost, "/chatik/api/send", query, payload, false)
	if err != nil {
		return "", "", fmt.Errorf("reply outcome may be uncertain: %w; %s; backup %s", err, inspect, backup.Name())
	}
	var receipt Object
	if err := json.Unmarshal(body, &receipt); err != nil {
		return "", "", fmt.Errorf("decode reply receipt: %w; %s; backup %s", err, inspect, backup.Name())
	}
	messageID := nativeID(receipt["id"])
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || id <= lastID {
		return "", "", fmt.Errorf("reply receipt has invalid or reused message ID %q; %s; backup %s", messageID, inspect, backup.Name())
	}
	return messageID, backup.Name(), nil
}
