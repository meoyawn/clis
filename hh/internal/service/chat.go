package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

func decodeChatState(body []byte) (Object, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse chat page: %w", err)
	}
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "template" && strings.Contains(" "+attr(n, "class")+" ", " Chatik-InitialState ") {
			found = textContent(n)
			return
		}
		for child := n.FirstChild; child != nil && found == ""; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if found == "" {
		return nil, fmt.Errorf("chat page has no Chatik-InitialState: authentication or page contract changed")
	}
	var state Object
	if err := json.Unmarshal([]byte(found), &state); err != nil {
		return nil, fmt.Errorf("decode Chatik-InitialState: %w", err)
	}
	return state, nil
}

type applicationChat struct {
	chat      Object
	resume    string
	sourceURL string
}

func (c *Client) readApplicationChat(ctx context.Context, status *ApplicationStatus) (*applicationChat, error) {
	if status == nil || (!status.Verified && !status.vacancyUnavailable) || !vacancyNumber.MatchString(status.ChatID) || !vacancyNumber.MatchString(status.ApplicantID) {
		return nil, fmt.Errorf("chat read requires a verified application with valid chat/applicant IDs")
	}
	query := url.Values{"without_list": {"1"}, "platform": {"xhh"}, "theme": {"hh-day"}, "dest": {"iframe"}, "microphone": {"true"}}
	path := "/chat/" + status.ChatID
	body, err := c.request(ctx, http.MethodGet, path, query, nil, true)
	if err != nil {
		return nil, err
	}
	state, err := decodeChatState(body)
	if err != nil {
		return nil, err
	}
	data, _ := state["chatData"].(map[string]any)
	chat, _ := data["chat"].(map[string]any)
	resources, _ := data["resources"].(map[string]any)
	topics, _ := resources["negotiation_topics"].(map[string]any)
	topic, _ := topics[status.TopicID].(map[string]any)
	resume := nativeID(topic["resumeId"])
	hashes, _ := resources["resumeHashById"].(map[string]any)
	chatResources, _ := chat["resources"].(map[string]any)
	if nativeID(chat["id"]) != status.ChatID || chat["type"] != "NEGOTIATION" || nativeID(chat["currentParticipantId"]) != status.ApplicantID ||
		nativeID(topic["topicId"]) != status.TopicID || nativeID(topic["vacancyId"]) != status.VacancyID || status.ResumeID != "" && hashes[resume] != status.ResumeID ||
		!hasNativeID(chatResources["NEGOTIATION_TOPIC"], status.TopicID) || !hasNativeID(chatResources["VACANCY"], status.VacancyID) || !hasNativeID(chatResources["RESUME"], resume) {
		return nil, fmt.Errorf("chat %s does not confirm the exact applicant, topic, vacancy and resume", status.ChatID)
	}
	if status.vacancyUnavailable {
		owner, _ := chat["ownerEmployerInfo"].(map[string]any)
		if resume != status.NativeResumeID || nativeID(owner["id"]) != nativeID(status.Employer["id"]) || topic["currentApplicantState"] != status.State {
			return nil, fmt.Errorf("chat %s does not confirm the deleted topic's resume, employer and outcome", status.ChatID)
		}
		if hash, ok := hashes[resume].(string); ok && hash != "" {
			id, err := NewResumeIDFromValue(hash)
			if err != nil {
				return nil, err
			}
			status.ResumeID = hash
			status.ResumeURL = strings.TrimSuffix(status.VacancyURL, "/vacancy/"+status.VacancyID) + "/resume/" + id.String() + "?print=true"
		}
		status.Verified = true
	}
	return &applicationChat{chat: chat, resume: resume, sourceURL: c.base.String() + path + "?" + query.Encode()}, nil
}

// ReadApplicationMessages exports visible messages from the verified chat.
// GET only: it does not open a chat, mark messages read, or send anything.
// has_more explicitly identifies an incomplete older-message history.
func (c *Client) ReadApplicationMessages(ctx context.Context, status *ApplicationStatus) error {
	result, err := c.readApplicationChat(ctx, status)
	if err != nil {
		return err
	}
	messages, _ := result.chat["messages"].(map[string]any)
	items, ok := messages["items"].([]any)
	hasMore, moreKnown := messages["hasMore"].(bool)
	if !ok || !moreKnown {
		return fmt.Errorf("chat %s has no message list or pagination flag", status.ChatID)
	}
	visible := []Object{}
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok || nativeID(message["chatId"]) != status.ChatID || !vacancyNumber.MatchString(nativeID(message["id"])) {
			return fmt.Errorf("chat %s contains an invalid or foreign message", status.ChatID)
		}
		if message["hidden"] != false {
			continue
		}
		row := Object{}
		for _, key := range []string{"id", "creationTime", "text", "type", "participantId", "participantDisplay", "workflowTransition", "resources"} {
			if value, exists := message[key]; exists {
				row[key] = value
			}
		}
		row["from_applicant"] = nativeID(message["participantId"]) == status.ApplicantID
		visible = append(visible, row)
	}
	status.Messages = Object{"items": visible, "has_more": hasMore, "source_url": result.sourceURL}
	return nil
}

// ReadApplicationLetter runs on an explicitly configured chat-origin client.
// The status comes from the independently verified vacancy-origin client.
// It reads the recorded chat page without opening/marking messages or posting.
func (c *Client) ReadApplicationLetter(ctx context.Context, status *ApplicationStatus) error {
	if status == nil || status.CoverLetter["present"] != true {
		return fmt.Errorf("letter read requires an application with a confirmed cover letter")
	}
	result, err := c.readApplicationChat(ctx, status)
	if err != nil {
		return err
	}
	chat, resume := result.chat, result.resume
	messages, _ := chat["messages"].(map[string]any)
	items, ok := messages["items"].([]any)
	if !ok {
		return fmt.Errorf("chat %s has no recorded message list", status.ChatID)
	}
	var selected Object
	for _, item := range items {
		message, _ := item.(map[string]any)
		linked, _ := message["resources"].(map[string]any)
		letters, _ := linked["RESPONSE_LETTER"].([]any)
		if len(letters) == 0 {
			continue // a regular chat message is not the application cover letter
		}
		transition, _ := message["workflowTransition"].(map[string]any)
		if nativeID(message["chatId"]) != status.ChatID || nativeID(message["participantId"]) != status.ApplicantID || message["hidden"] != false || message["hasContent"] != true ||
			!hasNativeID(linked["NEGOTIATION_TOPIC"], status.TopicID) || !hasNativeID(linked["VACANCY"], status.VacancyID) || !hasNativeID(linked["RESUME"], resume) ||
			nativeID(transition["topicId"]) != status.TopicID {
			continue
		}
		if selected != nil {
			return fmt.Errorf("chat %s has multiple matching cover-letter messages; no default selected", status.ChatID)
		}
		selected = message
	}
	if selected == nil {
		return fmt.Errorf("chat %s has no matching response-letter message in its returned page (hasMore=%v); no text inferred", status.ChatID, messages["hasMore"])
	}
	text, ok := selected["text"].(string)
	if !ok || strings.TrimSpace(text) == "" || !vacancyNumber.MatchString(nativeID(selected["id"])) {
		return fmt.Errorf("chat %s cover-letter message has no valid ID/nonempty text", status.ChatID)
	}
	canEdit, editKnown := selected["canEdit"].(bool)
	status.CoverLetter["text_available"] = true
	status.CoverLetter["text"] = text
	status.CoverLetter["source_url"] = result.sourceURL
	status.CoverLetter["message_id"] = nativeID(selected["id"])
	status.CoverLetter["current_text_readback_verified"] = true
	if editKnown {
		status.CoverLetter["can_edit_reported_by_chat"] = canEdit
	}
	return nil
}
