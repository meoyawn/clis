package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"golang.org/x/net/html"
)

type CompletionPlan struct {
	ID                    string   `json:"id"`
	ViewURL               string   `json:"view_url"`
	Title                 any      `json:"title"`
	ProfessionalRole      any      `json:"professional_role"`
	AccessType            any      `json:"access_type"`
	Status                any      `json:"status"`
	Searchable            bool     `json:"searchable"`
	Applied               bool     `json:"applied"`
	Verified              bool     `json:"verified"`
	BackupPath            string   `json:"backup_path,omitempty"`
	OtherResumesUnchanged []string `json:"other_resumes_unchanged,omitempty"`
	PrintText             string   `json:"print_text,omitempty"`
}

// Complete confirms the existing profession of a fully populated native clone.
// The observed builder has one remaining screen. Completing it can publish the
// resume under its existing access settings; this requires user authorization.
// Other builder flows are deliberately unsupported, not answered automatically.
func (c *Client) Complete(ctx context.Context, id ResumeID, metadata CloneMetadata, apply bool, backupDir string) (*CompletionPlan, error) {
	if err := c.validateBrowserMetadata(metadata); err != nil {
		return nil, err
	}
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	before := map[string]Object{}
	var selected *Resume
	for _, currentID := range ids {
		resume, err := c.Get(ctx, currentID)
		if err != nil {
			return nil, err
		}
		before[currentID.String()] = isolatedState(resume.Fields)
		if currentID == id {
			selected = resume
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("resume %s is not in the selected account; no write attempted", id)
	}
	attrs, _ := selected.Fields["_attributes"].(map[string]any)
	if attrs["permission"] != "edit" || attrs["status"] != "not_finished" || attrs["hasErrors"] != false {
		return nil, fmt.Errorf("resume %s is not an editable, valid unfinished draft (status %v); no write attempted", id, attrs["status"])
	}
	body, err := c.request(ctx, http.MethodGet, "/profile/resume/professional_role", url.Values{"resume": {id.String()}}, nil, true)
	if err != nil {
		return nil, err
	}
	scheme, err := decodeCompletionScheme(body)
	if err != nil {
		return nil, err
	}
	builderResume, _ := scheme["resume"].(map[string]any)
	builderAttrs, _ := builderResume["_attributes"].(map[string]any)
	screens, _ := scheme["screens"].([]any)
	builderFields, selectedBuilderFields := Editable(builderResume), Editable(selected.Fields)
	// Compare the two values this request sends across adapters. Other fields
	// differ in representation between APIs and are verified against the same
	// edit API before/after the write, including legacy proftest metadata.
	var changed []string
	for _, key := range []string{"title", "professionalRole"} {
		if !reflect.DeepEqual(selectedBuilderFields[key], builderFields[key]) {
			changed = append(changed, key)
		}
	}
	if builderAttrs["hash"] != id.String() || scheme["nextIncompleteScreenId"] != "professional_role" || len(screens) != 1 || builderAttrs["canPublishOrUpdate"] != true || len(changed) != 0 {
		return nil, fmt.Errorf("resume %s builder is not the observed single profession-confirmation flow or content changed (hash %v, next %v, screens %d, ready %v, fields %v); inspect it, no write attempted", id, builderAttrs["hash"], scheme["nextIncompleteScreenId"], len(screens), builderAttrs["canPublishOrUpdate"], changed)
	}
	fields := Editable(selected.Fields)
	roles, roleOK := fields["professionalRole"].([]any)
	titles, titleOK := fields["title"].([]any)
	if !roleOK || len(roles) != 1 || !titleOK || len(titles) != 1 || fmt.Sprint(roles[0]) == "" || fmt.Sprint(titles[0]) == "" {
		return nil, fmt.Errorf("resume %s requires one existing title and professionalRole; no write attempted", id)
	}
	view, err := c.View(ctx, id)
	if err != nil {
		return nil, err
	}
	profileBefore, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	profile, ok := profileBefore["profile"].(map[string]any)
	if !ok || profile["fields"] == nil {
		return nil, fmt.Errorf("shared profile fields unavailable; no write attempted")
	}
	plan := &CompletionPlan{ID: id.String(), ViewURL: view.URL, Title: fields["title"], ProfessionalRole: fields["professionalRole"], AccessType: fields["accessType"], Status: attrs["status"], PrintText: view.Text}
	if !apply {
		return plan, nil
	}
	if backupDir == "" {
		return nil, fmt.Errorf("completion backup directory is empty; no write attempted")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	backup, err := os.CreateTemp(filepath.Clean(backupDir), id.String()+"-complete-*.json")
	if err != nil {
		return nil, err
	}
	plan.BackupPath = backup.Name()
	encodeErr := json.NewEncoder(backup).Encode(Object{"selected": selected, "resumes": before, "profile": profileBefore})
	closeErr := backup.Close()
	if encodeErr != nil {
		return nil, encodeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	current, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(before[id.String()], isolatedState(current.Fields)) {
		return nil, fmt.Errorf("resume %s changed during completion preflight; no write attempted", id)
	}
	endpoint, _ := url.Parse("https://resume-profile-front.hh.ru/profile/shards/resume/update")
	response, err := c.nativeResumeRequest(ctx, endpoint, Object{
		"resumeHash": id.String(), "currentScreenId": "professional_role",
		"resume":  Object{"professionalRole": []string{fmt.Sprint(roles[0])}, "title": titles},
		"profile": Object{}, "questionToAnswerMap": Object{},
		"additionalProperties": Object{"anyJob": false}, "userPrint": metadata.UserPrint,
	}, metadata)
	if err != nil {
		return nil, fmt.Errorf("%w; inspect get/view before retrying; backup: %s", err, plan.BackupPath)
	}
	plan.Applied = true
	receiptResume, _ := response["resume"].(map[string]any)
	receiptAttrs, _ := receiptResume["_attributes"].(map[string]any)
	next, nextPresent := response["nextIncompleteScreenId"]
	if !nextPresent || next != nil || receiptAttrs["hash"] != id.String() {
		return nil, fmt.Errorf("completion returned no finished receipt for %s (next screen %v); inspect before retrying; backup: %s", id, next, plan.BackupPath)
	}
	for _, currentID := range ids {
		after, err := c.Get(ctx, currentID)
		if err != nil {
			return nil, fmt.Errorf("completion readback failed: %w; do not retry", err)
		}
		if !reflect.DeepEqual(before[currentID.String()], isolatedState(after.Fields)) {
			return nil, fmt.Errorf("ISOLATION FAILURE: resume %s content changed during completion; backup: %s", currentID, plan.BackupPath)
		}
		if currentID == id {
			afterAttrs, _ := after.Fields["_attributes"].(map[string]any)
			plan.Status = afterAttrs["status"]
			plan.Searchable, _ = afterAttrs["isSearchable"].(bool)
			if plan.Status == nil || plan.Status == "not_finished" {
				return nil, fmt.Errorf("resume %s is still unfinished after completion; inspect, do not retry", id)
			}
		} else {
			plan.OtherResumesUnchanged = append(plan.OtherResumesUnchanged, currentID.String())
		}
	}
	profileAfter, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	afterProfile, _ := profileAfter["profile"].(map[string]any)
	if !reflect.DeepEqual(profileComparisonValue(profile["fields"]), profileComparisonValue(afterProfile["fields"])) {
		return nil, fmt.Errorf("ISOLATION FAILURE: shared profile fields changed during completion; backup: %s", plan.BackupPath)
	}
	afterView, err := c.View(ctx, id)
	if err != nil {
		return nil, err
	}
	visibleFields := Object{}
	for _, key := range []string{"title", "skills", "keySkills", "experience", "email"} {
		if value, exists := fields[key]; exists {
			visibleFields[key] = value
		}
	}
	if err := verifyView(afterView, visibleFields); err != nil {
		return nil, err
	}
	plan.Verified, plan.PrintText = true, afterView.Text
	return plan, nil
}

// Native profile responses reorder resume-assignment and logo sets between
// reads. Compare every member/value while preserving ordered experience rows.
func profileComparisonValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		out := Object{}
		for key, child := range item {
			out[key] = profileComparisonValue(child)
			if key == "resumes" || key == "logo" {
				if rows, ok := out[key].([]any); ok {
					sort.Slice(rows, func(i, j int) bool {
						left, _ := json.Marshal(rows[i])
						right, _ := json.Marshal(rows[j])
						return bytes.Compare(left, right) < 0
					})
				}
			}
		}
		return out
	case []any:
		out := make([]any, len(item))
		for i, child := range item {
			out[i] = profileComparisonValue(child)
		}
		return out
	default:
		return value
	}
}

func decodeCompletionScheme(body []byte) (Object, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse resume builder: %w", err)
	}
	var scheme Object
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "template" {
			var state Object
			if json.Unmarshal([]byte(textContent(n)), &state) == nil {
				if value, ok := state["scheme"].(map[string]any); ok {
					scheme = value
					return
				}
			}
		}
		for child := n.FirstChild; child != nil && scheme == nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if scheme == nil {
		return nil, fmt.Errorf("resume builder scheme unavailable: authentication, checkpoint or changed format; no write attempted")
	}
	return scheme, nil
}
