package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/domain"
)

const launchPostEdit = "com.linkedin.sdui.requests.sharing.LaunchUpdatePost"
const launchPostEditWithMedia = "com.linkedin.sdui.requests.sharing.LaunchUpdatePostWithMedia"
const updateNormShare = "com.linkedin.sdui.sharing.requests.UpdateNormShare"

type postEditOptions struct {
	apply                                                        bool
	expectedAuthor, expectedHash, expectedCurrentHash, recordDir string
}

type postEditResult struct {
	Author         person      `json:"author"`
	Post           domain.Post `json:"post"`
	Text           string      `json:"text"`
	SHA256         string      `json:"sha256"`
	CurrentSHA256  string      `json:"current_sha256"`
	Edited         bool        `json:"edited"`
	Verified       bool        `json:"verified"`
	AlreadyPresent bool        `json:"already_present"`
	RecordPath     string      `json:"record_path,omitempty"`
}

type postEditPlan struct {
	action map[string]any
	states map[string]map[string]any
	data   documentData
}

func preparePostEdit(post domain.Post, draft domain.PostDraft, response []byte) (postEditPlan, error) {
	data := readFlightData(string(response))
	plan := postEditPlan{states: map[string]map[string]any{}, data: data}
	initialAction := launchPostEdit
	if len(post.ImageAssetIDs) != 0 {
		initialAction = launchPostEditWithMedia
	}
	actions := map[string]map[string]any{}
	var conflict bool
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] == "proto.sdui.actions.core.ServerRequest" && jsonStringAt(m, "value", "requestId") == initialAction {
				a, _ := m["value"].(map[string]any)
				b, _ := json.Marshal(a)
				actions[string(b)] = a
			}
			states, _ := m["modelStates"].([]any)
			for _, item := range states {
				s, _ := item.(map[string]any)
				key := jsonStringAt(s, "key", "key", "value", "id")
				if key == "" {
					continue
				}
				if old := plan.states[key]; old != nil {
					a, _ := json.Marshal(old["value"])
					b, _ := json.Marshal(s["value"])
					if string(a) != string(b) {
						conflict = true
					}
				}
				plan.states[key] = s
			}
		})
	}
	if conflict || len(actions) != 1 {
		return plan, fmt.Errorf("edit form has conflicting states or %d update actions", len(actions))
	}
	for _, a := range actions {
		plan.action = a
	}
	for key, expected := range map[string]string{"ShareBoxshareBoxMode": "Edit", "ShareBoxselectedActor": "Self", "ShareBoxeditingPostUrnBinding": post.URN} {
		if jsonStringAt(plan.states[key], "value", "stringValue") != expected {
			return plan, fmt.Errorf("edit form %q does not confirm %q", key, expected)
		}
	}
	if len(post.ImageAssetIDs) != 0 && (len(post.ImageAssetIDs) != 1 || jsonStringAt(plan.states["ShareBoxassetIdBinding"], "value", "stringValue") != post.ImageAssetIDs[0]) {
		return plan, fmt.Errorf("edit form does not confirm the existing image asset")
	}
	if jsonStringAt(plan.states["ShareBoxrichCommentaryTextMemoryBinding"], "value", "textModelForWrite", "text") != post.Text {
		return plan, fmt.Errorf("edit form text differs from the exact post readback")
	}
	textState := plan.states["ShareBoxrichCommentaryTextMemoryBinding"]
	textState["value"] = map[string]any{"$case": "textModelForWrite", "textModelForWrite": map[string]any{"$type": "proto.sdui.components.core.text.TextModel", "text": draft.Text(), "attributesV2": []any{}}}
	// The native Save trigger sets this before launching its update request.
	if lifecycle := plan.states["ShareBoxshareLifeCycleState"]; lifecycle != nil {
		lifecycle["value"] = map[string]any{"$case": "stringValue", "stringValue": "ShareLifeCycleState_PUBLISHED"}
	}
	return plan, nil
}

// Translate only the state keys explicitly requested by the observed action.
// originalProtoCase retains the website's model-state type, not a guessed binding.
func (p postEditPlan) requestedStates(action map[string]any) ([]any, error) {
	args, _ := action["requestedArguments"].(map[string]any)
	keys, _ := args["requestedStateKeys"].([]any)
	result := make([]any, 0, len(keys))
	for _, k := range keys {
		key := jsonStringAt(k, "key", "value", "id")
		s := p.states[key]
		if s == nil {
			// The website's d3 state serializer omits uninitialized bindings.
			continue
		}
		value, _ := s["value"].(map[string]any)
		kind, _ := value["$case"].(string)
		v, exists := value[kind]
		if !exists {
			return nil, fmt.Errorf("edit state %q has unsupported value %q", key, kind)
		}
		switch kind {
		case "textModelForWrite":
			v = map[string]any{"$type": "TextModel", "text": jsonStringAt(v, "text"), "attribute": []any{}, "source": "local"}
		case "signedLongValue", "longValue":
			raw, ok := v.(string)
			if !ok || !strings.HasPrefix(raw, "$n") {
				return nil, fmt.Errorf("edit state %q has an unsupported long value", key)
			}
			v = map[string]any{"type": "bigint", "value": strings.TrimPrefix(raw, "$n")}
		case "stringValue", "intValue", "floatValue", "booleanValue", "mediaListValue", "imageAssetValue", "dateValue", "colorValue", "textModel":
			v = p.resolveValue(v, 0)
		case "stringListValue":
			list, _ := v.(map[string]any)
			v = list["stringValues"]
		default:
			return nil, fmt.Errorf("edit state %q uses unsupported type %q", key, kind)
		}
		keyObject, _ := k.(map[string]any)
		namespace := "MemoryNamespace"
		if object, ok := k.(map[string]any); ok {
			if explicit, ok := object["namespace"].(string); ok {
				namespace = explicit
			}
		}
		protoKey, _ := keyObject["key"].(map[string]any)
		protoKey["$type"] = "proto.sdui.Key"
		result = append(result, map[string]any{"key": key, "namespace": namespace, "value": v, "originalProtoCase": kind, "protoKey": protoKey})
	}
	return result, nil
}

func (p postEditPlan) resolveValue(value any, depth int) any {
	if depth > 64 {
		return value
	}
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(v, "$") {
			if ref, ok := p.data[strings.TrimPrefix(v, "$")]; ok {
				return p.resolveValue(ref, depth+1)
			}
		}
	case map[string]any:
		result := map[string]any{}
		for k, child := range v {
			result[k] = p.resolveValue(child, depth+1)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = p.resolveValue(child, depth+1)
		}
		return result
	}
	return value
}

func editResponse(data []byte) (documentData, map[string]any, error) {
	rows := readFlightData(string(data))
	root, _ := rows["0"].(map[string]any)
	response, _ := root["response"].(map[string]any)
	errorsList, ok := response["errors"].([]any)
	if !ok || len(errorsList) != 0 || response["completionAction"] == nil {
		return nil, nil, fmt.Errorf("edit action has no error-free completion")
	}
	return rows, response, nil
}

func (c *linkedinClient) applyPostEdit(ctx context.Context, ref domain.PostRef, author person, before domain.Post, draft domain.PostDraft, plan postEditPlan, opts postEditOptions) (postEditResult, error) {
	currentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(before.Text)))
	result := postEditResult{Author: author, Post: before, Text: draft.Text(), SHA256: draft.SHA256(), CurrentSHA256: currentHash}
	if !opts.apply {
		return result, nil
	}
	if opts.expectedAuthor != author.URL || opts.expectedHash != draft.SHA256() {
		return result, fmt.Errorf("post edit author or replacement text changed; preview again")
	}
	if before.Text == draft.Text() {
		result.AlreadyPresent, result.Edited, result.Verified = true, true, true
		return result, nil
	}
	if opts.expectedCurrentHash != currentHash {
		return result, fmt.Errorf("post text changed since preview; no edit sent")
	}
	if err := os.MkdirAll(opts.recordDir, 0700); err != nil {
		return result, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(ref.URN()+"\x00"+draft.SHA256())))
	result.RecordPath = filepath.Join(opts.recordDir, key+".json")
	f, err := os.OpenFile(result.RecordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return result, fmt.Errorf("previous edit attempt is unconfirmed; inspect %q and the post before retrying; no update sent", result.RecordPath)
	}
	if err != nil {
		return result, err
	}
	writeErr := writeJSON(f, map[string]any{"before": before, "author": author.URL, "text": draft.Text(), "sha256": draft.SHA256(), "attempted_at": time.Now().UTC().Format(time.RFC3339Nano)})
	syncErr, closeErr := f.Sync(), f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return result, err
	}
	action := plan.action
	for step := 0; step < 3; step++ {
		states, err := plan.requestedStates(action)
		if err != nil {
			return result, err
		}
		response, err := c.postNativeAction(ctx, action, states)
		if err != nil {
			return result, fmt.Errorf("edit outcome unconfirmed; inspect post and %q before retrying: %w", result.RecordPath, err)
		}
		if err := os.WriteFile(fmt.Sprintf("%s.%d.receipt", result.RecordPath, step), response, 0600); err != nil {
			return result, err
		}
		rows, receipt, err := editResponse(response)
		if err != nil {
			return result, err
		}
		if action["requestId"] == updateNormShare {
			break
		}
		next := map[string]map[string]any{}
		rows.walk(receipt["completionAction"], func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.ServerRequest" {
				return
			}
			a, _ := m["value"].(map[string]any)
			b, _ := json.Marshal(a)
			next[string(b)] = a
		})
		if len(next) == 0 {
			break
		}
		if len(next) != 1 || step == 2 {
			return result, fmt.Errorf("edit completion has unsupported follow-up actions; inspect %q", result.RecordPath)
		}
		for _, a := range next {
			action = a
		}
		id, _ := action["requestId"].(string)
		if id != updateNormShare {
			return result, fmt.Errorf("edit follow-up %q is unsupported; inspect %q", id, result.RecordPath)
		}
		// A final update must bind both the exact target and replacement text.
		states, err = plan.requestedStates(action)
		if err != nil {
			return result, err
		}
		var target, text bool
		for _, s := range states {
			m, _ := s.(map[string]any)
			if m["key"] == "ShareBoxeditingPostUrnBinding" && m["value"] == ref.URN() {
				target = true
			}
			if m["key"] == "ShareBoxrichCommentaryTextMemoryBinding" && jsonStringAt(m, "value", "text") == draft.Text() {
				text = true
			}
		}
		if !target || !text {
			return result, fmt.Errorf("edit follow-up does not bind exact target and text; no final update sent")
		}
	}
	actual, err := c.readPost(ctx, ref, nil)
	if err != nil {
		return result, fmt.Errorf("edit sent but readback failed; inspect before retrying: %w", err)
	}
	if actual.AuthorURL != author.URL || actual.Text != draft.Text() || !samePostImages(before.ImageAssetIDs, actual.ImageAssetIDs) {
		return result, fmt.Errorf("edit readback does not match author, text and original image; inspect %q before retrying", result.RecordPath)
	}
	result.Post, result.Edited, result.Verified = actual, true, true
	if err := os.WriteFile(result.RecordPath+".verified.json", mustJSON(result), 0600); err != nil {
		return result, err
	}
	return result, nil
}

func samePostImages(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

func mustJSON(value any) []byte {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(fmt.Errorf("encode internal post edit record: %w", err))
	}
	return data
}
