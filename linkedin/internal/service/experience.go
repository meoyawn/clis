package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const positionSaveRequest = "com.linkedin.sdui.requests.profile.saveProfilePositionForm"

var positionIDPattern = regexp.MustCompile("^[1-9][0-9]*$")

// ExperienceCapture contains the native form contract, never HAR credentials.
type ExperienceCapture struct {
	ProfileID      string
	PositionID     string
	VanityName     string
	Description    string
	Path           string
	body           map[string]any
	descriptionKey string
	originalKey    string
}

// LoadExperienceCapture accepts one successful native save for the exact position.
func LoadExperienceCapture(r io.Reader, positionID string) (ExperienceCapture, error) {
	if !positionIDPattern.MatchString(positionID) {
		return ExperienceCapture{}, fmt.Errorf("invalid position ID %q: expected positive decimal digits", positionID)
	}
	data, err := io.ReadAll(io.LimitReader(r, (64<<20)+1))
	if err != nil {
		return ExperienceCapture{}, fmt.Errorf("read experience HAR: %w", err)
	}
	if len(data) > 64<<20 {
		return ExperienceCapture{}, fmt.Errorf("experience HAR exceeds 64 MiB")
	}
	var har struct {
		Log struct {
			Entries []struct {
				Request struct {
					Method   string
					URL      string
					PostData struct{ Text string }
				}
				Response struct {
					Status  int
					Content struct{ Text string }
				}
			}
		}
	}
	if err := json.Unmarshal(data, &har); err != nil {
		return ExperienceCapture{}, fmt.Errorf("decode experience HAR: %w", err)
	}
	var matches []ExperienceCapture
	for _, entry := range har.Log.Entries {
		u, err := url.Parse(entry.Request.URL)
		if err != nil || u.Scheme != "https" || u.Host != "www.linkedin.com" || u.User != nil ||
			u.Path != "/flagship-web/rsc-action/actions/server-request" || entry.Request.Method != "POST" {
			continue
		}
		var body map[string]any
		decoder := json.NewDecoder(strings.NewReader(entry.Request.PostData.Text))
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil || body["requestId"] != positionSaveRequest {
			continue
		}
		args, ok := body["requestedArguments"].(map[string]any)
		if !ok {
			return ExperienceCapture{}, fmt.Errorf("native save missing requestedArguments")
		}
		payload, ok := args["payload"].(map[string]any)
		if !ok {
			return ExperienceCapture{}, fmt.Errorf("native save missing payload")
		}
		if payload["positionId"] != positionID {
			continue
		}
		if entry.Response.Status != 200 {
			return ExperienceCapture{}, fmt.Errorf("captured position %s save was HTTP %d", positionID, entry.Response.Status)
		}
		if err := ValidateExperienceResponse([]byte(entry.Response.Content.Text)); err != nil {
			return ExperienceCapture{}, fmt.Errorf("captured position %s save: %w", positionID, err)
		}
		for key := range u.Query() {
			if key != "sduiid" && key != "parentSpanId" {
				return ExperienceCapture{}, fmt.Errorf("unsupported native save query field %q", key)
			}
		}
		profile, _ := payload["profileId"].(string)
		vanity, _ := payload["vanityName"].(string)
		if profile == "" || vanity == "" || strings.ContainsAny(vanity, "/?#") {
			return ExperienceCapture{}, fmt.Errorf("captured position %s has invalid owner identifiers", positionID)
		}
		server, ok := body["serverRequest"].(map[string]any)
		if !ok || server["requestId"] != positionSaveRequest {
			return ExperienceCapture{}, fmt.Errorf("captured position %s has inconsistent save request IDs", positionID)
		}
		serverArgs, ok := server["requestedArguments"].(map[string]any)
		if !ok {
			return ExperienceCapture{}, fmt.Errorf("native save missing server arguments")
		}
		a, _ := json.Marshal(serverArgs["payload"])
		b, _ := json.Marshal(payload)
		if !bytes.Equal(a, b) {
			return ExperienceCapture{}, fmt.Errorf("native save payload copies disagree")
		}
		descriptionKey, err := bindingKey(payload, "description")
		if err != nil {
			return ExperienceCapture{}, err
		}
		originalKey, err := bindingKey(payload, "originalDescription")
		if err != nil {
			return ExperienceCapture{}, err
		}
		broadcastKey, err := bindingKey(payload, "allowProfileEditBroadcasts")
		if err != nil {
			return ExperienceCapture{}, err
		}
		description, err := consistentState(body, descriptionKey)
		if err != nil {
			return ExperienceCapture{}, err
		}
		text, ok := description.(string)
		if !ok {
			return ExperienceCapture{}, fmt.Errorf("native description state is not text")
		}
		if _, err := consistentState(body, originalKey); err != nil {
			return ExperienceCapture{}, err
		}
		broadcast, err := consistentState(body, broadcastKey)
		if err != nil {
			return ExperienceCapture{}, err
		}
		if broadcast != false {
			return ExperienceCapture{}, fmt.Errorf("native save enables network notifications; capture with Notify network off")
		}
		matches = append(matches, ExperienceCapture{ProfileID: profile, PositionID: positionID, VanityName: vanity,
			Description: text, Path: u.RequestURI(), body: body, descriptionKey: descriptionKey, originalKey: originalKey})
	}
	if len(matches) != 1 {
		return ExperienceCapture{}, fmt.Errorf("HAR must contain exactly one successful native save for position %s; found %d", positionID, len(matches))
	}
	return matches[0], nil
}

func bindingKey(payload map[string]any, name string) (string, error) {
	value, ok := payload[name].(map[string]any)
	if !ok || value["namespace"] != "MemoryNamespace" {
		return "", fmt.Errorf("unsupported native %s binding", name)
	}
	key, ok := value["key"].(string)
	if !ok || key == "" {
		return "", fmt.Errorf("native %s binding has no key", name)
	}
	return key, nil
}

func visitStates(value any, visit func(map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "states" {
				if states, ok := child.([]any); ok {
					for _, state := range states {
						if row, ok := state.(map[string]any); ok {
							visit(row)
						}
					}
				}
			}
			visitStates(child, visit)
		}
	case []any:
		for _, child := range value {
			visitStates(child, visit)
		}
	}
}

func consistentState(body map[string]any, key string) (any, error) {
	var values []any
	visitStates(body, func(state map[string]any) {
		if state["key"] == key && state["namespace"] == "MemoryNamespace" {
			values = append(values, state["value"])
		}
	})
	if len(values) == 0 {
		return nil, fmt.Errorf("native state %q missing", key)
	}
	first, _ := json.Marshal(values[0])
	for _, value := range values[1:] {
		next, _ := json.Marshal(value)
		if !bytes.Equal(first, next) {
			return nil, fmt.Errorf("native state %q copies disagree", key)
		}
	}
	return values[0], nil
}

// PrepareDescription changes only the description and its comparison baseline.
func (c ExperienceCapture) PrepareDescription(text string) ([]byte, error) {
	if c.body == nil {
		return nil, fmt.Errorf("experience capture has no native form; load a successful Save HAR")
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(utf16.Encode([]rune(text))) > 2000 {
		return nil, fmt.Errorf("description must contain 1–2000 UTF-16 code units of valid text")
	}
	data, err := json.Marshal(c.body)
	if err != nil {
		return nil, fmt.Errorf("encode native form: %w", err)
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("clone native form: %w", err)
	}
	visitStates(body, func(state map[string]any) {
		switch state["key"] {
		case c.descriptionKey:
			state["value"] = text
		case c.originalKey:
			state["value"] = c.Description
		}
	})
	return json.Marshal(body)
}

// ValidateExperienceResponse checks the native RSC receipt, not just HTTP 200.
func ValidateExperienceResponse(data []byte) error {
	for _, line := range bytes.Split(data, []byte("\n")) {
		jsonLine, ok := bytes.CutPrefix(line, []byte("0:"))
		if !ok {
			continue
		}
		var envelope struct {
			Response *struct {
				Errors           *[]json.RawMessage
				CompletionAction *struct{ Actions []json.RawMessage }
			}
		}
		if err := json.Unmarshal(jsonLine, &envelope); err != nil {
			return fmt.Errorf("invalid native save receipt: %w", err)
		}
		if envelope.Response == nil || envelope.Response.Errors == nil || len(*envelope.Response.Errors) != 0 ||
			envelope.Response.CompletionAction == nil || len(envelope.Response.CompletionAction.Actions) == 0 {
			return fmt.Errorf("native save did not confirm an error-free completion")
		}
		return nil
	}
	return fmt.Errorf("native save response has no RSC completion receipt")
}
