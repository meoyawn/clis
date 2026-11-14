package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// UpdateApplicationLetter uses the native Chatik edit action, not a new message
// or application. The caller supplies a vacancy-verified application status.
func (c *Client) UpdateApplicationLetter(ctx context.Context, status *ApplicationStatus, letter string, apply bool, backupDir string) (Object, error) {
	if !utf8.ValidString(letter) {
		return nil, fmt.Errorf("letter is not valid UTF-8; no edit attempted")
	}
	letter = strings.TrimSpace(strings.ReplaceAll(letter, "\r\n", "\n"))
	if letter == "" {
		return nil, fmt.Errorf("letter must be nonempty; no edit attempted")
	}
	if err := c.ReadApplicationLetter(ctx, status); err != nil {
		return nil, err
	}
	before := status.CoverLetter["text"].(string)
	message := status.CoverLetter["message_id"].(string)
	result := Object{"vacancy_id": status.VacancyID, "topic_id": status.TopicID,
		"chat_id": status.ChatID, "resume_id": status.ResumeID, "message_id": message,
		"previous_text": before, "text": letter, "changed": before != letter, "applied": false}
	if before == letter {
		result["verified"] = true
		return result, nil
	}
	if status.CoverLetter["can_edit_reported_by_chat"] != true {
		return nil, fmt.Errorf("cover-letter message %s is not confirmed editable; no edit attempted", message)
	}
	if !apply {
		return result, nil
	}
	if backupDir == "" {
		return nil, fmt.Errorf("backup directory is empty; no edit attempted")
	}
	id, err := strconv.ParseInt(message, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cover-letter message ID %q: %w", message, err)
	}
	snapshot, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode previous application letter: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, fmt.Errorf("create letter backup directory %q: %w", backupDir, err)
	}
	backup, err := os.CreateTemp(backupDir, "letter-"+status.TopicID+"-*.json")
	if err != nil {
		return nil, fmt.Errorf("create letter backup: %w", err)
	}
	_, writeErr := backup.Write(snapshot)
	closeErr := backup.Close()
	if writeErr != nil {
		return nil, fmt.Errorf("write letter backup %q: %w", backup.Name(), writeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close letter backup %q: %w", backup.Name(), closeErr)
	}
	result["backup_path"] = backup.Name()
	// The native save route has no response schema. Only a fresh, independently
	// bound chat read can confirm that this specific letter was changed.
	if _, err := c.request(ctx, http.MethodPost, "/chatik/api/save", nil, Object{"text": letter, "messageId": id}, false); err != nil {
		return nil, fmt.Errorf("letter edit outcome may be uncertain: %w; inspect application letter before retrying; backup %s", err, backup.Name())
	}
	if err := c.ReadApplicationLetter(ctx, status); err != nil {
		return nil, fmt.Errorf("letter save returned successfully but verification failed: %w; do not resend", err)
	}
	if status.CoverLetter["message_id"] != message || status.CoverLetter["text"] != letter {
		return nil, fmt.Errorf("chat readback differs after saving cover-letter message %s; inspect application letter before retrying", message)
	}
	result["applied"], result["verified"] = true, true
	return result, nil
}
