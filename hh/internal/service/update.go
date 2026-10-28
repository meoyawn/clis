package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// These fields come from HH's resume-scoped update adapter in the recorded
// App bundle. Profile endpoints are intentionally not used for resume updates.
var stringFields = map[string]bool{
	"lang": true, "title": true, "email": true, "photo": true, "skills": true, "keySkills": true,
	"portfolio": true, "accessType": true, "travelTime": true, "workFormats": true, "hiddenFields": true,
	"setkaAccess": true, "autoHideTime": true, "educationLevel": true, "employmentForms": true,
	"professionalRole": true, "preferredContact": true, "businessTripReadiness": true,
}

var objectFields = map[string]bool{
	"phone": true, "salary": true, "proftest": true, "experience": true, "certificate": true,
	"personalSite": true, "recommendation": true, "primaryEducation": true, "additionalEducation": true,
	"elementaryEducation": true, "attestationEducation": true,
}

func NormalizePatch(patch Object) (Object, error) {
	if len(patch) == 0 {
		return nil, fmt.Errorf("invalid patch: expected at least one resume field")
	}
	out := make(Object, len(patch))
	for field, value := range patch {
		if !stringFields[field] && !objectFields[field] {
			return nil, fmt.Errorf("unsupported patch field %q: see hh --help for supported resume fields; no write attempted", field)
		}
		if value == nil {
			return nil, fmt.Errorf("invalid patch field %q: use an empty array to clear, not null", field)
		}
		if text, ok := value.(string); ok && stringFields[field] {
			value = []any{text}
		}
		items, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("invalid patch field %q: expected an array%s", field, func() string {
				if stringFields[field] {
					return " (a single string is also accepted)"
				}
				return ""
			}())
		}
		for i, item := range items {
			if stringFields[field] {
				switch item.(type) {
				case string:
				case float64, bool:
					if field == "title" || field == "skills" || field == "email" || field == "keySkills" {
						return nil, fmt.Errorf("invalid %s[%d]: expected string", field, i)
					}
				default:
					return nil, fmt.Errorf("invalid %s[%d]: expected scalar", field, i)
				}
			} else if _, ok := item.(map[string]any); !ok {
				return nil, fmt.Errorf("invalid %s[%d]: expected object", field, i)
			}
		}
		if field == "keySkills" && len(items) > 30 {
			return nil, fmt.Errorf("invalid keySkills count %d: hh.ru permits at most 30", len(items))
		}
		if field == "experience" {
			for i, item := range items {
				x := item.(map[string]any)
				for _, key := range []string{"companyName", "position", "startDate", "description"} {
					if _, ok := x[key].(string); !ok {
						return nil, fmt.Errorf("invalid experience[%d].%s: expected string", i, key)
					}
				}
			}
			value = editableExperience(items)
		}
		out[field] = value
	}
	return out, nil
}

// Editable returns the wire representation of fields supported by the native
// resume endpoint, dropping derived status/experience intervals and UI labels.
func Editable(fields Object) Object {
	out := make(Object)
	for key, value := range fields {
		if !stringFields[key] && !objectFields[key] {
			continue
		}
		if stringFields[key] {
			var values = []any{}
			if items, ok := value.([]any); ok {
				for _, item := range items {
					if obj, ok := item.(map[string]any); ok {
						if text, ok := obj["string"]; ok {
							values = append(values, text)
						}
					} else if _, ok := item.(string); ok {
						values = append(values, item)
					}
				}
			}
			out[key] = values
		} else {
			out[key] = value
			if key == "experience" {
				if rows, ok := value.([]any); ok {
					out[key] = editableExperience(rows)
				}
			}
		}
	}
	return out
}

func editableExperience(rows []any) []any {
	result := make([]any, 0, len(rows))
	for _, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			result = append(result, row)
			continue
		}
		clean := make(Object, len(object))
		for key, value := range object {
			switch key {
			case "companyLogos", "interval", "companyAreaTitle", "companyState", "employerId", "industries", "professionId", "professionName", "verification":
				continue
			}
			clean[key] = value
		}
		result = append(result, clean)
	}
	return result
}

func isolatedState(fields Object) Object {
	state := Editable(fields)
	// Proficiency is enriched from the profile and is not a writable resume
	// field, but it must still be preserved and checked for unintended changes.
	state["resumeApplicantSkills"] = fields["resumeApplicantSkills"]
	return state
}

// HH exposes the union of skills currently selected by any resume. Removing
// the last use hides the inventory row, but reselecting it restores its saved
// rating (verified against the live account on 2026-10-28). Only such unused
// SKILL rows may disappear; retained ratings and language rows must not change.
func unusedSkillNames(before *Resume, patch Object, others map[string]Object) map[string]bool {
	selected, changed := patch["keySkills"].([]any)
	if !changed {
		return nil
	}
	used := map[string]bool{}
	for _, value := range selected {
		used[fmt.Sprint(value)] = true
	}
	for _, other := range others {
		values, _ := other["keySkills"].([]any)
		for _, value := range values {
			used[fmt.Sprint(value)] = true
		}
	}
	unused := map[string]bool{}
	rows, _ := before.Fields["resumeApplicantSkills"].([]any)
	for _, row := range rows {
		item, ok := row.(map[string]any)
		if !ok || item["category"] != "SKILL" {
			continue
		}
		name, _ := item["name"].(string)
		if name != "" && !used[name] {
			unused[name] = true
		}
	}
	return unused
}

func sameSkillInventory(before, after any, mayDisappear map[string]bool) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	oldRows, oldOK := before.([]any)
	newRows, newOK := after.([]any)
	if !oldOK || !newOK {
		return false
	}
	remaining := map[string]any{}
	for _, row := range newRows {
		item, ok := row.(map[string]any)
		if !ok {
			return false
		}
		name, _ := item["name"].(string)
		if name == "" || remaining[name] != nil {
			return false
		}
		remaining[name] = row
	}
	for _, row := range oldRows {
		item, ok := row.(map[string]any)
		if !ok {
			return false
		}
		name, _ := item["name"].(string)
		current, exists := remaining[name]
		if !exists {
			if !mayDisappear[name] {
				return false
			}
		} else if !reflect.DeepEqual(row, current) {
			return false
		}
		delete(remaining, name)
	}
	return len(remaining) == 0
}

func sameIsolatedState(before Object, after *Resume, mayDisappear map[string]bool) bool {
	current := isolatedState(after.Fields)
	if !sameSkillInventory(before["resumeApplicantSkills"], current["resumeApplicantSkills"], mayDisappear) {
		return false
	}
	current["resumeApplicantSkills"] = before["resumeApplicantSkills"]
	return reflect.DeepEqual(before, current)
}

type Change struct {
	Before any `json:"before"`
	After  any `json:"after"`
}
type Plan struct {
	ID                    string            `json:"id"`
	ViewURL               string            `json:"view_url"`
	Applied               bool              `json:"applied"`
	Verified              bool              `json:"verified"`
	Changes               map[string]Change `json:"changes"`
	OtherResumesUnchanged []string          `json:"other_resumes_unchanged,omitempty"`
	PrintText             string            `json:"print_text,omitempty"`
	BackupPath            string            `json:"backup_path,omitempty"`
}

func MakePlan(resume *Resume, patch Object) *Plan {
	plan := &Plan{ID: resume.ID, ViewURL: resume.ViewURL, Changes: make(map[string]Change)}
	before := Editable(resume.Fields)
	for key, value := range patch {
		if !reflect.DeepEqual(before[key], value) {
			plan.Changes[key] = Change{Before: before[key], After: value}
		}
	}
	return plan
}

func (c *Client) Update(ctx context.Context, id ResumeID, patch Object, apply bool, backupDir string) (*Plan, error) {
	patch, err := NormalizePatch(patch)
	if err != nil {
		return nil, err
	}
	before, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if rows, ok := patch["experience"].([]any); ok {
		known := map[string]bool{}
		existingRows, _ := before.Fields["experience"].([]any)
		for _, row := range existingRows {
			if item, ok := row.(map[string]any); ok {
				known[fmt.Sprint(item["id"])] = true
			}
		}
		for i, row := range rows {
			item := row.(map[string]any)
			if rowID := item["id"]; rowID != nil && !known[fmt.Sprint(rowID)] {
				return nil, fmt.Errorf("experience[%d] ID %v is not in selected resume %s; export fresh data; no write attempted", i, rowID, id)
			}
		}
	}
	plan := MakePlan(before, patch)
	if !apply || len(plan.Changes) == 0 {
		return plan, nil
	}
	attrs, _ := before.Fields["_attributes"].(map[string]any)
	if attrs["permission"] != "edit" {
		return nil, fmt.Errorf("resume %s has permission %v, expected edit; no write attempted", id, attrs["permission"])
	}
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	found := false
	others := map[string]Object{}
	for _, other := range ids {
		if other == id {
			found = true
			continue
		}
		res, err := c.Get(ctx, other)
		if err != nil {
			return nil, err
		}
		others[other.String()] = isolatedState(res.Fields)
	}
	if !found {
		return nil, fmt.Errorf("selected resume %s is not in this account's resume list; no write attempted", id)
	}
	unusedSkills := unusedSkillNames(before, patch, others)
	if backupDir == "" {
		return nil, fmt.Errorf("backup directory is empty; no write attempted")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(Object{"selected": before, "other_resumes": others}, "", "  ")
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Clean(backupDir), id.String()+"-*.json")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	plan.BackupPath = file.Name()
	current, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(isolatedState(current.Fields), isolatedState(before.Fields)) {
		return nil, fmt.Errorf("resume %s changed while preparing update; rerun preview; no write attempted", id)
	}
	changed := make(Object, len(plan.Changes))
	for key := range plan.Changes {
		changed[key] = patch[key]
	}
	if _, err := c.Apply(ctx, id, changed); err != nil {
		return nil, fmt.Errorf("%w; backup: %s", err, plan.BackupPath)
	}
	plan.Applied = true
	after, err := c.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("write completed but verification failed: %w; backup: %s", err, plan.BackupPath)
	}
	if err := VerifyPatch(after.Fields, changed); err != nil {
		return nil, fmt.Errorf("%w; backup: %s", err, plan.BackupPath)
	}
	// Unpatched fields, including key skills and manual proficiency levels, must
	// survive. HH's derived status/interval metadata is excluded by Editable.
	afterFields := Editable(after.Fields)
	beforeFields := Editable(before.Fields)
	if !sameSkillInventory(before.Fields["resumeApplicantSkills"], after.Fields["resumeApplicantSkills"], unusedSkills) {
		return nil, fmt.Errorf("profile proficiency levels changed unexpectedly; stop; backup: %s", plan.BackupPath)
	}
	for key, value := range beforeFields {
		if _, patched := changed[key]; !patched && !reflect.DeepEqual(value, afterFields[key]) {
			return nil, fmt.Errorf("unrequested field %q changed in resume %s; stop; backup: %s", key, id, plan.BackupPath)
		}
	}
	for hash, old := range others {
		other, _ := NewResumeIDFromValue(hash)
		res, err := c.Get(ctx, other)
		if err != nil {
			return nil, fmt.Errorf("write completed but other-resume verification failed: %w", err)
		}
		if !sameIsolatedState(old, res, unusedSkills) {
			return nil, fmt.Errorf("ISOLATION FAILURE: non-target resume %s changed; stop; backup: %s", hash, plan.BackupPath)
		}
		plan.OtherResumesUnchanged = append(plan.OtherResumesUnchanged, hash)
	}
	sort.Strings(plan.OtherResumesUnchanged)
	view, err := c.View(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("write read back successfully but print-view verification failed: %w", err)
	}
	if err := verifyView(view, changed); err != nil {
		return nil, fmt.Errorf("%w; backup: %s", err, plan.BackupPath)
	}
	plan.Verified = true
	plan.PrintText = view.Text
	return plan, nil
}

// Verify checks an existing result without reapplying a request, including after
// a timeout or native cloning of profile-backed experience IDs.
func (c *Client) Verify(ctx context.Context, id ResumeID, patch Object) (*Plan, error) {
	patch, err := NormalizePatch(patch)
	if err != nil {
		return nil, err
	}
	resume, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := VerifyPatch(resume.Fields, patch); err != nil {
		return nil, err
	}
	view, err := c.View(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := verifyView(view, patch); err != nil {
		return nil, err
	}
	return &Plan{ID: id.String(), ViewURL: view.URL, Verified: true, Changes: map[string]Change{}, PrintText: view.Text}, nil
}

func verifyView(view *View, changed Object) error {
	if err := VerifyPatch(view.Fields, changed); err != nil {
		return fmt.Errorf("print view differs from saved edit data: %w", err)
	}
	// Check visible prose too: a fresh embedded state alone is not evidence
	// that HH actually rendered the text the employer is going to read.
	visible := strings.Join(strings.Fields(view.Text), " ")
	for _, field := range []string{"skills", "title", "email"} {
		if values, ok := changed[field].([]any); ok {
			for _, value := range values {
				if text, ok := value.(string); ok && !strings.Contains(visible, strings.Join(strings.Fields(text), " ")) {
					return fmt.Errorf("requested %q text is absent from employer print view", field)
				}
			}
		}
	}
	if rows, ok := changed["experience"].([]any); ok {
		for i, row := range rows {
			item := row.(map[string]any)
			for _, field := range []string{"companyName", "position", "description"} {
				text, _ := item[field].(string)
				if text != "" && !strings.Contains(visible, strings.Join(strings.Fields(text), " ")) {
					return fmt.Errorf("experience[%d].%s is absent from employer print view", i, field)
				}
			}
		}
	}
	return nil
}

func (c *Client) Apply(ctx context.Context, id ResumeID, patch Object) (Object, error) {
	data, err := c.request(ctx, http.MethodPost, "/applicant/resume/edit", url.Values{"resume": {id.String()}, "hhtmSource": {"resume_partial_edit"}}, patch, false)
	if err != nil {
		return nil, err
	}
	var result Object
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode update response for %s: %w; verify print view before retrying", id, err)
	}
	if errors, ok := result["errors"]; ok && errors != nil && !reflect.DeepEqual(errors, Object{}) && !reflect.DeepEqual(errors, []any{}) {
		return nil, fmt.Errorf("HH rejected resume %s: %s", id, boundedResponse(data))
	}
	return result, nil
}

func VerifyPatch(actual Object, patch Object) error {
	actual = Editable(actual)
	var failures []string
	for key, want := range patch {
		got := actual[key]
		if key == "experience" {
			got = experienceContent(got)
			want = experienceContent(want)
		}
		if !containsValues(got, want) {
			failures = append(failures, key)
		}
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		return fmt.Errorf("saved resume does not match requested fields %v; stopped, do not retry blindly", failures)
	}
	return nil
}

func experienceContent(value any) any {
	rows, ok := value.([]any)
	if !ok {
		return value
	}
	rows = editableExperience(rows)
	for _, row := range rows {
		if item, ok := row.(map[string]any); ok {
			delete(item, "id")
		}
	}
	return rows
}

// HH enriches object rows with IDs/derived fields. Compare the requested keys
// recursively while retaining array length/order so dropped rows cannot pass.
func containsValues(actual, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range w {
			if !containsValues(a[key], value) {
				return false
			}
		}
		return true
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(w) {
			return false
		}
		for i, value := range w {
			if !containsValues(a[i], value) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(actual, want)
	}
}

// ResumeIDs accepts the native list response without assuming two resumes or
// an order. Identity is always the public hash, never numeric profile row IDs.
func ResumeIDs(list Object) ([]ResumeID, error) {
	items, ok := list["resumes"].([]any)
	if !ok {
		return nil, fmt.Errorf("resumes list has unexpected type %T", list["resumes"])
	}
	var ids []ResumeID
	seen := map[string]bool{}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid resumes[%d]: expected object", i)
		}
		var hash string
		for _, key := range []string{"hash", "resumeHash"} {
			if text, ok := obj[key].(string); ok {
				hash = text
				break
			}
		}
		if hash == "" {
			if attrs, ok := obj["_attributes"].(map[string]any); ok {
				hash, _ = attrs["hash"].(string)
			}
		}
		id, err := NewResumeIDFromValue(hash)
		if err != nil {
			return nil, fmt.Errorf("resumes[%d] identity: %w", i, err)
		}
		if !seen[id.String()] {
			ids = append(ids, id)
			seen[id.String()] = true
		}
	}
	return ids, nil
}
