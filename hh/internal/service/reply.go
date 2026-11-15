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
	chatID, err := strconv.ParseInt(status.ChatID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid chat ID %q: %w", status.ChatID, err)
	}
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, fmt.Errorf("generate message idempotency key: %w", err)
	}
	key[6], key[8] = key[6]&0x0f|0x40, key[8]&0x3f|0x80
	idempotencyKey := fmt.Sprintf("%x-%x-%x-%x-%x", key[:4], key[4:6], key[6:8], key[8:10], key[10:])
	payload := Object{"chatId": chatID, "idempotencyKey": idempotencyKey, "text": text}
	snapshot, err := json.MarshalIndent(Object{"review": review, "request": payload, "messages": status.Messages}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode reply backup: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, fmt.Errorf("create reply backup directory %q: %w", backupDir, err)
	}
	backup, err := os.CreateTemp(backupDir, "reply-"+status.TopicID+"-*.json")
	if err != nil {
		return nil, fmt.Errorf("create reply backup: %w", err)
	}
	_, writeErr := backup.Write(snapshot)
	closeErr := backup.Close()
	if writeErr != nil {
		return nil, fmt.Errorf("write reply backup %q: %w", backup.Name(), writeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close reply backup %q: %w", backup.Name(), closeErr)
	}
	query := url.Values{"hhtmSourceLabel": {"chat"}}
	body, err := c.request(ctx, http.MethodPost, "/chatik/api/send", query, payload, false)
	if err != nil {
		return nil, fmt.Errorf("reply outcome may be uncertain: %w; inspect application get --messages before retrying; backup %s", err, backup.Name())
	}
	var receipt Object
	if err := json.Unmarshal(body, &receipt); err != nil {
		return nil, fmt.Errorf("decode reply receipt: %w; inspect application get --messages before retrying; backup %s", err, backup.Name())
	}
	messageID := nativeID(receipt["id"])
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || id <= lastID {
		return nil, fmt.Errorf("reply receipt has invalid or reused message ID %q; inspect application get --messages before retrying; backup %s", messageID, backup.Name())
	}
	if err := c.ReadApplicationMessages(ctx, status); err != nil {
		return nil, fmt.Errorf("reply returned message %s but chat verification failed: %w; do not resend; backup %s", messageID, err, backup.Name())
	}
	for _, message := range status.Messages["items"].([]Object) {
		if nativeID(message["id"]) == messageID && message["from_applicant"] == true && message["text"] == text {
			review["sent"], review["verified"] = true, true
			review["message_id"], review["backup_path"] = messageID, backup.Name()
			return review, nil
		}
	}
	return nil, fmt.Errorf("reply message %s text or author not confirmed by chat readback; do not resend; backup %s", messageID, backup.Name())
}
