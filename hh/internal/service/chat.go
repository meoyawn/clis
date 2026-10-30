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

// ReadApplicationLetter runs on an explicitly configured chat-origin client.
// The status comes from the independently verified vacancy-origin client.
// It reads the recorded chat page without opening/marking messages or posting.
func (c *Client) ReadApplicationLetter(ctx context.Context, status *ApplicationStatus) error {
	if status == nil || !status.Verified || !vacancyNumber.MatchString(status.ChatID) || !vacancyNumber.MatchString(status.ApplicantID) {
		return fmt.Errorf("letter read requires a verified application with valid chat/applicant IDs")
	}
	if status.CoverLetter["present"] != true {
		return fmt.Errorf("application topic %s has no confirmed cover letter", status.TopicID)
	}
	query := url.Values{"without_list": {"1"}, "platform": {"xhh"}, "theme": {"hh-day"}, "dest": {"iframe"}, "microphone": {"true"}}
	path := "/chat/" + status.ChatID
	body, err := c.request(ctx, http.MethodGet, path, query, nil, true)
	if err != nil {
		return err
	}
	state, err := decodeChatState(body)
	if err != nil {
		return err
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
		nativeID(topic["topicId"]) != status.TopicID || nativeID(topic["vacancyId"]) != status.VacancyID || hashes[resume] != status.ResumeID ||
		!hasNativeID(chatResources["NEGOTIATION_TOPIC"], status.TopicID) || !hasNativeID(chatResources["VACANCY"], status.VacancyID) || !hasNativeID(chatResources["RESUME"], resume) {
		return fmt.Errorf("chat %s does not confirm the exact applicant, topic, vacancy and resume; no letter returned", status.ChatID)
	}
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
	status.CoverLetter["source_url"] = c.base.String() + path + "?" + query.Encode()
	status.CoverLetter["message_id"] = nativeID(selected["id"])
	status.CoverLetter["current_text_readback_verified"] = true
	if editKnown {
		status.CoverLetter["can_edit_reported_by_chat"] = canEdit
	}
	return nil
}
