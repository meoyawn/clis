package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/meoyawn/clis/twitter/internal/domain"
	"github.com/meoyawn/clis/twitter/internal/service"
)

type chatPreview struct {
	Account        domain.Profile                `json:"account"`
	Availability   service.MessagingAvailability `json:"availability"`
	ConversationID string                        `json:"conversation_id"`
	Text           string                        `json:"text"`
	SHA256         string                        `json:"package_sha256"`
	AlreadyPresent bool                          `json:"already_present"`
	Sent           bool                          `json:"sent"`
	Verified       bool                          `json:"verified"`
	Message        *service.ChatMessage          `json:"message,omitempty"`
	AttemptPath    string                        `json:"attempt_path,omitempty"`
	CheckedAt      time.Time                     `json:"checked_at"`
}

func readChat(ctx context.Context, args []string, dir string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("messages read requires one handle; got %d arguments", len(args))
	}
	handle, err := domain.Handle(args[0])
	if err != nil {
		return err
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	if _, err := c.Bootstrap(ctx); err != nil {
		return err
	}
	recipient, err := c.Profile(ctx, handle)
	if err != nil {
		return err
	}
	page, err := c.ReadChat(ctx, recipient)
	if err != nil {
		return err
	}
	return writeJSON(out, page)
}

func chatPackageHash(owner, recipient, text string) string {
	encoded, _ := json.Marshal([]string{"plain-dm", owner, recipient, text})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func chatCommand(ctx context.Context, args []string, dir string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("messages send", flag.ContinueOnError)
	fs.SetOutput(errOut)
	textFile := fs.String("text-file", "", "exact UTF-8 message file")
	send := fs.Bool("send", false, "send the reviewed message once")
	expectAccount := fs.String("expect-account", "", "reviewed sender handle")
	expectHash := fs.String("expect-sha256", "", "reviewed package_sha256")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *textFile == "" {
		return fmt.Errorf("messages send requires --text-file FILE and flags followed by one handle")
	}
	handle, err := domain.Handle(fs.Arg(0))
	if err != nil {
		return err
	}
	if *send && (*expectAccount == "" || *expectHash == "") {
		return fmt.Errorf("--send requires --expect-account and --expect-sha256 from the preview")
	}
	data, err := os.ReadFile(*textFile)
	if err != nil {
		return err
	}
	messageText := string(data)
	if !utf8.Valid(data) || strings.TrimSpace(messageText) == "" || len(data) > 10000 {
		return fmt.Errorf("message file %q must contain nonblank UTF-8 text, at most 10000 bytes", *textFile)
	}
	c, err := loadClient(dir)
	if err != nil {
		return err
	}
	owner, err := c.Bootstrap(ctx)
	if err != nil {
		return err
	}
	if *send && !strings.EqualFold(strings.TrimPrefix(*expectAccount, "@"), owner.Handle) {
		return fmt.Errorf("sender does not match --expect-account %q", *expectAccount)
	}
	availability, err := c.MessagingAvailability(ctx, handle)
	if err != nil {
		return err
	}
	if availability.CanMessage == nil || !*availability.CanMessage || availability.CanDMOnXChat == nil || *availability.CanDMOnXChat {
		details, _ := json.Marshal(availability.MessagePermissions)
		return fmt.Errorf("recipient %q does not explicitly allow ordinary unencrypted DMs: %s", handle, details)
	}
	hash := chatPackageHash(owner.ID, availability.Recipient.Profile.ID, messageText)
	if *send && hash != *expectHash {
		return fmt.Errorf("message sender, recipient or text changed; review a new preview")
	}
	page, err := c.ReadChat(ctx, availability.Recipient.Profile)
	if err != nil {
		return err
	}
	if page.HasKeys {
		return fmt.Errorf("conversation %q contains encryption keys; refusing an unencrypted send", page.ConversationID)
	}
	result := chatPreview{Account: owner, Availability: availability, ConversationID: page.ConversationID, Text: messageText, SHA256: hash, CheckedAt: time.Now().UTC()}
	for _, message := range page.Messages {
		if message.SenderID == owner.ID && !message.Encrypted && message.Text == messageText {
			result.AlreadyPresent, result.Verified = true, true
			result.Message = &message
			return writeJSON(out, result)
		}
	}
	if !*send {
		return writeJSON(out, result)
	}
	history := filepath.Join(dir, "messages", owner.ID)
	if err := os.MkdirAll(history, 0700); err != nil {
		return err
	}
	path := filepath.Join(history, hash+".json")
	result.AttemptPath = path
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	id[6], id[8] = (id[6]&15)|64, (id[8]&63)|128
	messageID := fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
	result.Message = &service.ChatMessage{ID: messageID, SenderID: owner.ID, ConversationID: page.ConversationID, Text: messageText}
	attempt, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("message attempt exists or cannot be saved at %q; inspect it before retrying: %w", path, err)
	}
	writeErr := writeJSON(attempt, result)
	if writeErr == nil {
		writeErr = attempt.Sync()
	}
	if err := errors.Join(writeErr, attempt.Close()); err != nil {
		return err
	}
	receipt, err := os.OpenFile(path+".receipt.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer receipt.Close()
	message, err := c.SendChat(ctx, availability, page, messageID, messageText, receipt)
	if err != nil {
		return fmt.Errorf("send unconfirmed; inspect %q and read the conversation before retrying: %w", path, err)
	}
	result.Sent, result.Message = true, &message
	if err := receipt.Sync(); err != nil {
		return err
	}
	after, err := c.ReadChat(ctx, availability.Recipient.Profile)
	if err != nil {
		return fmt.Errorf("message %q accepted, readback failed; do not resend: %w", message.ID, err)
	}
	for _, found := range after.Messages {
		if found.ID == message.ID && found.SenderID == owner.ID && found.ConversationID == page.ConversationID && found.Text == messageText {
			result.Verified = true
		}
	}
	if !result.Verified {
		return fmt.Errorf("message %q accepted but absent from readback; do not resend", message.ID)
	}
	result.CheckedAt = time.Now().UTC()
	confirmed, err := os.OpenFile(path+".confirmed.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer confirmed.Close()
	if err := writeJSON(confirmed, result); err != nil {
		return err
	}
	return writeJSON(out, result)
}
