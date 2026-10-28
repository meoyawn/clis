package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/meoyawn/clis/linkedin/internal/service"
	"golang.org/x/net/html"
)

const saveAbout = "com.linkedin.sdui.requests.profile.saveProfileAboutForm"
const savePosition = "com.linkedin.sdui.requests.profile.saveProfilePositionForm"
const uploadPositionMedia = "com.linkedin.sdui.impl.profile.components.forms.uploadPositionMedia"
const uploadProjectMedia = "com.linkedin.sdui.impl.profile.components.forms.uploadProjectMedia"
const saveProject = "com.linkedin.sdui.requests.profile.saveProfileProjectForm"

type profileFormSpec struct {
	path, screen, initialRequest, saveRequest, fieldRequest, entityID string
}

type profileFormResult struct {
	ProfileURL string         `json:"profile_url"`
	Before     map[string]any `json:"before"`
	After      map[string]any `json:"after"`
	Applied    bool           `json:"applied"`
	Verified   bool           `json:"verified"`
	RecordPath string         `json:"record_path,omitempty"`
}

func readProfileForm(document string) (postEditPlan, error) {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return postEditPlan{}, err
	}
	return profileFormData(readDocumentData(doc))
}

func profileFormData(data documentData) (postEditPlan, error) {
	form := postEditPlan{data: data, states: map[string]map[string]any{}}
	var conflict string
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			states, _ := m["modelStates"].([]any)
			for _, item := range states {
				state, _ := item.(map[string]any)
				key := jsonStringAt(state, "key", "key", "value", "id")
				if key == "" {
					continue
				}
				if old := form.states[key]; old != nil && !reflect.DeepEqual(old["value"], state["value"]) {
					conflict = key
				}
				form.states[key] = state
			}
		})
	}
	if conflict != "" || len(form.states) == 0 {
		return form, fmt.Errorf("profile form has no states or conflicting state %q", conflict)
	}
	return form, nil
}

func profileFormAction(data documentData, requestID string) (map[string]any, error) {
	actions := map[string]map[string]any{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.ServerRequest" || jsonStringAt(m, "value", "requestId") != requestID {
				return
			}
			action, _ := m["value"].(map[string]any)
			encoded, _ := json.Marshal(action["requestedArguments"])
			actions[string(encoded)] = action
		})
	}
	if len(actions) != 1 {
		return nil, fmt.Errorf("profile form exposes %d distinct %q actions", len(actions), requestID)
	}
	for _, action := range actions {
		return action, nil
	}
	return nil, fmt.Errorf("profile action %q is missing", requestID)
}

func checkFormOwner(action map[string]any, owner profileResult) error {
	if jsonStringAt(action, "requestedArguments", "payload", "profileId") != strings.TrimPrefix(owner.ProfileURN, "urn:li:fsd_profile:") ||
		"https://www.linkedin.com/in/"+jsonStringAt(action, "requestedArguments", "payload", "vanityName")+"/" != owner.URL {
		return fmt.Errorf("native form does not bind the signed-in profile")
	}
	return nil
}

func profileFieldKey(action map[string]any, field string) (string, error) {
	args, _ := action["requestedArguments"].(map[string]any)
	payload, _ := args["payload"].(map[string]any)
	return serviceBindingKey(payload, field)
}

func serviceBindingKey(payload map[string]any, field string) (string, error) {
	key := jsonStringAt(payload, field, "key")
	if key == "" || jsonStringAt(payload, field, "namespace") != "MemoryNamespace" {
		return "", fmt.Errorf("native profile field %q has no supported binding", field)
	}
	return key, nil
}

func profileStateValue(form postEditPlan, key string) any {
	v, _ := form.states[key]["value"].(map[string]any)
	kind, _ := v["$case"].(string)
	if kind == "expression" {
		ids, err := form.formExpression(v[kind], 0)
		if err == nil {
			return ids
		}
	}
	return form.resolveValue(v[kind], 0)
}

func (p postEditPlan) formExpression(value any, depth int) (any, error) {
	if depth > 24 {
		return nil, fmt.Errorf("native form expression exceeds the depth limit")
	}
	m, _ := value.(map[string]any)
	if child, ok := m["expression"]; ok {
		return p.formExpression(child, depth+1)
	}
	kind, _ := m["$case"].(string)
	switch kind {
	case "booleanExpression", "stringExpression", "stringListExpression":
		return p.formExpression(m[kind], depth+1)
	case "stringValue", "booleanValue":
		return m[kind], nil
	case "bindableString":
		key := jsonStringAt(m[kind], "key", "key", "value", "id")
		text, ok := profileStateValue(p, key).(string)
		if !ok {
			return nil, fmt.Errorf("native expression references missing string %q", key)
		}
		return text, nil
	case "notExpression", "orExpression", "stringEqualsExpression":
		operands, _ := m[kind].(map[string]any)
		a, err := p.formExpression(operands["a"], depth+1)
		if err != nil {
			return nil, err
		}
		if kind == "notExpression" {
			b, ok := a.(bool)
			if !ok {
				return nil, fmt.Errorf("native not expression requires a boolean")
			}
			return !b, nil
		}
		b, err := p.formExpression(operands["b"], depth+1)
		if err != nil {
			return nil, err
		}
		if kind == "orExpression" {
			left, leftOK := a.(bool)
			right, rightOK := b.(bool)
			if !leftOK || !rightOK {
				return nil, fmt.Errorf("native or expression requires booleans")
			}
			return left || right, nil
		}
		left, leftOK := a.(string)
		right, rightOK := b.(string)
		if !leftOK || !rightOK {
			return nil, fmt.Errorf("native string comparison requires strings")
		}
		if operands["ignoreCase"] == true {
			return strings.EqualFold(left, right), nil
		}
		return left == right, nil
	case "collectionIdsStringList":
		return p.collectionIDs(jsonStringAt(m[kind], "collectionId"))
	}
	return nil, fmt.Errorf("unsupported native form expression %q", kind)
}

func (p postEditPlan) collectionIDs(id string) ([]any, error) {
	if id == "" {
		return nil, fmt.Errorf("unsupported native collection expression")
	}
	lists := map[string][]any{}
	for _, row := range p.data {
		p.data.walk(row, func(m map[string]any) {
			if m["collectionId"] != id {
				return
			}
			items, ok := m["initialItems"].([]any)
			if !ok {
				return
			}
			ids := make([]any, 0, len(items))
			for _, item := range items {
				key := jsonStringAt(item, "key")
				if key == "" {
					return
				}
				ids = append(ids, key)
			}
			encoded, _ := json.Marshal(ids)
			lists[string(encoded)] = ids
		})
	}
	if len(lists) != 1 {
		return nil, fmt.Errorf("collection %q has %d distinct initial lists", id, len(lists))
	}
	for _, ids := range lists {
		return ids, nil
	}
	return nil, fmt.Errorf("collection %q has no initial list", id)
}

func setProfileState(form postEditPlan, key string, value any) error {
	state := form.states[key]
	if state == nil {
		return fmt.Errorf("native profile state %q is missing", key)
	}
	v, _ := state["value"].(map[string]any)
	kind, _ := v["$case"].(string)
	switch kind {
	case "stringValue", "booleanValue", "dateValue":
		state["value"] = map[string]any{"$case": kind, kind: value}
	default:
		return fmt.Errorf("native profile state %q has unsupported editable type %q", key, kind)
	}
	return nil
}

func profileFormFields(form postEditPlan, action map[string]any) map[string]any {
	args, _ := action["requestedArguments"].(map[string]any)
	payload, _ := args["payload"].(map[string]any)
	fields := map[string]any{}
	for name, binding := range payload {
		switch name {
		case "hasChanges", "progressIndicator", "userExitedForm", "saveOrDeleteRequestPending", "isLoading", "aiSuggestionTrackingId":
			continue
		}
		if jsonStringAt(binding, "namespace") == "MemoryNamespace" {
			fields[name] = profileStateValue(form, jsonStringAt(binding, "key"))
		}
	}
	return fields
}

func (c *linkedinClient) sendProfileAction(ctx context.Context, form postEditPlan, action map[string]any, screen string) ([]byte, error) {
	states, err := form.requestedStates(action)
	if err != nil {
		return nil, err
	}
	args, _ := action["requestedArguments"].(map[string]any)
	clientArgs := map[string]any{}
	for k, v := range args {
		clientArgs[k] = v
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = states, screen, []any{}
	id, _ := action["requestId"].(string)
	body, err := json.Marshal(map[string]any{"requestId": id, "serverRequest": action, "states": states, "requestedArguments": clientArgs})
	if err != nil {
		return nil, err
	}
	response, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/server-request?sduiid="+url.QueryEscape(id), bytes.NewReader(body), http.Header{
		"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"},
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("native profile response exceeds 16 MiB")
	}
	return data, err
}

func (c *linkedinClient) saveProfileForm(ctx context.Context, form postEditPlan, initial map[string]any, spec profileFormSpec, owner profileResult, record string) error {
	action := initial
	if spec.initialRequest != spec.saveRequest {
		data, err := c.sendProfileAction(ctx, form, action, spec.screen)
		if err != nil {
			return fmt.Errorf("native preparation outcome uncertain; inspect before retrying: %w", err)
		}
		if err := os.WriteFile(record+".prepare-receipt", data, 0600); err != nil {
			return err
		}
		if err := service.ValidateExperienceResponse(data); err != nil {
			return err
		}
		rows := readFlightData(string(data))
		action, err = profileFormAction(rows, spec.saveRequest)
		if err != nil {
			return err
		}
		if err := checkFormOwner(action, owner); err != nil {
			return err
		}
		for _, row := range rows {
			rows.walk(row, func(m map[string]any) {
				if m["$type"] == "proto.sdui.actions.core.SetState" {
					state, _ := m["value"].(map[string]any)
					s, _ := state["state"].(map[string]any)
					key := jsonStringAt(s, "key", "key", "value", "id")
					if key != "" && form.states[key] == nil {
						form.states[key] = s
					}
				}
			})
		}
	}
	if spec.entityID != "" && jsonStringAt(action, "requestedArguments", "payload", "positionId") != spec.entityID {
		return fmt.Errorf("native save does not bind position %q", spec.entityID)
	}
	data, err := c.sendProfileAction(ctx, form, action, spec.screen)
	if err != nil {
		return fmt.Errorf("profile save outcome uncertain; inspect before retrying: %w", err)
	}
	if err := os.WriteFile(record+".receipt", data, 0600); err != nil {
		return err
	}
	return service.ValidateExperienceResponse(data)
}

func (c *linkedinClient) editProfileForm(ctx context.Context, ref profileRef, spec profileFormSpec, changes map[string]any, apply bool, backupDir string) (profileFormResult, error) {
	result := profileFormResult{After: changes}
	self, err := c.mailbox(ctx)
	if err != nil {
		return result, err
	}
	before, err := c.profile(ctx, ref)
	if err != nil {
		return result, err
	}
	if before.ProfileURN != self {
		return result, fmt.Errorf("profile editing only supports the signed-in owner")
	}
	result.ProfileURL = before.URL
	document, err := c.profileDocument(ctx, before.URL+spec.path)
	if err != nil {
		return result, err
	}
	form, err := readProfileForm(document)
	if err != nil {
		return result, err
	}
	action, err := profileFormAction(form.data, spec.initialRequest)
	if err != nil {
		return result, err
	}
	if err := checkFormOwner(action, before); err != nil {
		return result, err
	}
	fieldsAction, err := profileFormAction(form.data, spec.fieldRequest)
	if err != nil {
		return result, err
	}
	fields := profileFormFields(form, fieldsAction)
	result.Before = map[string]any{}
	for field, value := range changes {
		key, err := profileFieldKey(fieldsAction, field)
		if err != nil {
			return result, err
		}
		result.Before[field] = profileStateValue(form, key)
		if err := setProfileState(form, key, value); err != nil {
			return result, err
		}
	}
	if reflect.DeepEqual(result.Before, changes) {
		result.Verified = true
		return result, nil
	}
	if spec.saveRequest == saveAbout {
		current, _ := result.Before["about"].(string)
		if strings.Join(strings.Fields(current), "") != strings.Join(strings.Fields(before.Summary), "") {
			return result, fmt.Errorf("about form differs from current profile; no write attempted")
		}
	}
	if key, err := profileFieldKey(fieldsAction, "allowProfileEditBroadcasts"); err == nil && profileStateValue(form, key) != false {
		return result, fmt.Errorf("native profile form enables network notifications; no write attempted")
	}
	if !apply {
		return result, nil
	}
	intro, err := c.profileDocument(ctx, before.URL+"edit/intro/")
	if err != nil {
		return result, err
	}
	introBefore, err := prepareHeadline(intro, before, before.Headline)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(backupDir, "profile-form-"+fmt.Sprint(time.Now().UnixNano())+"-*.json")
	if err != nil {
		return result, err
	}
	result.RecordPath = file.Name()
	writeErr := writeJSON(file, map[string]any{"profile": before, "form": document, "changes": changes, "intro_fields": introBefore.fields})
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return result, err
	}
	if err := c.saveProfileForm(ctx, form, action, spec, before, result.RecordPath); err != nil {
		return result, fmt.Errorf("profile update not verified; inspect %q before retrying: %w", result.RecordPath, err)
	}
	result.Applied = true
	after, err := c.profile(ctx, ref)
	if err != nil {
		return result, fmt.Errorf("profile saved; readback failed, do not resend: %w", err)
	}
	if !reflect.DeepEqual(before.person, after.person) || (spec.saveRequest != saveAbout && before.Summary != after.Summary) {
		return result, fmt.Errorf("other profile identity or About content changed; inspect %q", result.RecordPath)
	}
	afterDocument, err := c.profileDocument(ctx, after.URL+spec.path)
	if err != nil {
		return result, err
	}
	afterForm, err := readProfileForm(afterDocument)
	if err != nil {
		return result, err
	}
	afterAction, err := profileFormAction(afterForm.data, spec.fieldRequest)
	if err != nil {
		return result, err
	}
	afterFields := profileFormFields(afterForm, afterAction)
	for field, value := range changes {
		if !reflect.DeepEqual(afterFields[field], value) {
			return result, fmt.Errorf("saved profile field %q did not match; do not resend", field)
		}
		afterFields[field] = fields[field]
	}
	for _, field := range []string{"initialAbout", "originalDescription"} {
		if _, exists := fields[field]; exists {
			afterFields[field] = fields[field]
		}
	}
	if !reflect.DeepEqual(fields, afterFields) {
		return result, fmt.Errorf("unrequested form fields changed; inspect %q", result.RecordPath)
	}
	afterIntro, err := c.profileDocument(ctx, after.URL+"edit/intro/")
	if err != nil {
		return result, err
	}
	introAfter, err := prepareHeadline(afterIntro, after, after.Headline)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(introBefore.fields, introAfter.fields) {
		return result, fmt.Errorf("profile intro fields changed; inspect %q", result.RecordPath)
	}
	result.Verified = true
	if err := os.WriteFile(result.RecordPath+".verified.json", mustJSON(result), 0600); err != nil {
		return result, err
	}
	return result, nil
}

func profileBackupDir(store authStore) string {
	return filepath.Join(store.dir, "backups")
}
