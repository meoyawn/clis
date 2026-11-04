package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

func languageRows(value any) ([]any, error) {
	rows, ok := value.([]any)
	if !ok || len(rows) == 0 {
		return nil, fmt.Errorf("language must be a nonempty array of id/degree objects")
	}
	out := make([]any, 0, len(rows))
	seen := map[string]bool{}
	for i, value := range rows {
		row, _ := value.(map[string]any)
		id := fmt.Sprint(row["id"])
		n, err := strconv.ParseInt(id, 10, 64)
		degree, _ := row["degree"].(string)
		validDegree := degree == "l1" || degree == "a1" || degree == "a2" || degree == "b1" || degree == "b2" || degree == "c1" || degree == "c2"
		if len(row) != 2 || err != nil || n <= 0 || !validDegree || seen[id] {
			return nil, fmt.Errorf("invalid language[%d]: unique positive id and degree a1–c2/l1 required", i)
		}
		seen[id] = true
		out = append(out, Object{"id": id, "degree": degree})
	}
	return out, nil
}

func NormalizeLanguagesPatch(patch Object) (Object, error) {
	if len(patch) != 1 {
		return nil, fmt.Errorf("profile languages patch must contain only language; no write attempted")
	}
	rows, err := languageRows(patch["language"])
	if err != nil {
		return nil, err
	}
	return Object{"language": rows}, nil
}

func (c *Client) Languages(ctx context.Context) (Object, error) {
	p, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	fields, err := profileFields(p)
	if err != nil {
		return nil, err
	}
	rows, err := languageRows(fields["language"])
	if err != nil {
		return nil, err
	}
	return Object{"language": rows}, nil
}

func resumeExceptLanguages(fields Object) Object {
	out := isolatedState(fields)
	delete(out, "language")
	rows, _ := fields["resumeApplicantSkills"].([]any)
	retained := []any{}
	for _, value := range rows {
		row, _ := value.(map[string]any)
		if row["category"] != "LANG" {
			retained = append(retained, value)
		}
	}
	out["resumeApplicantSkills"] = retained
	return out
}

func verifyPrintedLanguages(view *View, fields Object, want []any) ([]string, error) {
	names := map[string]Object{}
	rows, _ := fields["resumeApplicantSkills"].([]any)
	for _, value := range rows {
		row, _ := value.(map[string]any)
		if row["category"] == "LANG" {
			names[fmt.Sprint(row["id"])] = row
		}
	}
	expected := []string{}
	for _, value := range want {
		row := value.(map[string]any)
		skill := names[row["id"].(string)]
		name, _ := skill["name"].(string)
		level, _ := skill["level"].(map[string]any)
		if name == "" || level["internalId"] != row["degree"] {
			return nil, fmt.Errorf("resume %s language %v name/proficiency readback differs; stopped", view.ID, row["id"])
		}
		if row["degree"] != "l1" {
			label, _ := level["name"].(string)
			if label == "" {
				return nil, fmt.Errorf("resume %s language %v has no level label", view.ID, row["id"])
			}
			name += " — " + label
		}
		expected = append(expected, name)
	}
	doc, err := html.Parse(bytes.NewReader(view.HTML))
	if err != nil {
		return nil, err
	}
	actual := []string{}
	var visit func(*html.Node, bool)
	visit = func(n *html.Node, inLanguages bool) {
		if n.Data == "script" || n.Data == "template" || n.Data == "style" || attr(n, "aria-hidden") == "true" {
			return
		}
		inLanguages = inLanguages || attr(n, "data-qa") == "resume-languages-block"
		if inLanguages && attr(n, "data-qa") == "resume-block-language-item" {
			actual = append(actual, strings.Join(strings.Fields(textContent(n)), " "))
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child, inLanguages)
		}
	}
	visit(doc, false)
	sort.Strings(actual)
	sort.Strings(expected)
	if !reflect.DeepEqual(actual, expected) {
		return nil, fmt.Errorf("resume %s rendered language list differs: got %v, expected %v; inspect before retrying", view.ID, actual, expected)
	}
	return actual, nil
}

func (c *Client) verifyLanguageResumes(ctx context.Context, ids []ResumeID, want []any, snapshots map[string]Object) ([]any, error) {
	evidence := []any{}
	for _, id := range ids {
		resume, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		got, err := languageRows(resume.Fields["language"])
		if err != nil || !reflect.DeepEqual(got, want) {
			return nil, fmt.Errorf("resume %s language selections differ from the requested shared profile; stopped", id)
		}
		if old, ok := snapshots[id.String()]; ok && !reflect.DeepEqual(old, resumeExceptLanguages(resume.Fields)) {
			return nil, fmt.Errorf("unrequested content or technical-skill levels changed in resume %s; stopped", id)
		}
		view, err := c.View(ctx, id)
		if err != nil {
			return nil, err
		}
		labels, err := verifyPrintedLanguages(view, resume.Fields, want)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, Object{"id": id.String(), "view_url": view.URL, "languages": labels})
	}
	return evidence, nil
}

func (c *Client) VerifyLanguages(ctx context.Context, patch Object) (Object, error) {
	patch, err := NormalizeLanguagesPatch(patch)
	if err != nil {
		return nil, err
	}
	saved, err := c.Languages(ctx)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(saved, patch) {
		return nil, fmt.Errorf("saved shared languages differ from the requested list; stopped")
	}
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	evidence, err := c.verifyLanguageResumes(ctx, ids, patch["language"].([]any), nil)
	if err != nil {
		return nil, err
	}
	return Object{"verified": true, "language": patch["language"], "affected_resumes": evidence}, nil
}

func (c *Client) UpdateLanguages(ctx context.Context, patch Object, apply bool, backupDir string) (Object, error) {
	patch, err := NormalizeLanguagesPatch(patch)
	if err != nil {
		return nil, err
	}
	before, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	fields, err := profileFields(before)
	if err != nil {
		return nil, err
	}
	old, err := languageRows(fields["language"])
	if err != nil {
		return nil, err
	}
	result := Object{"before": old, "after": patch["language"], "applied": false}
	if !apply {
		return result, nil
	}
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	snapshots := map[string]Object{}
	for _, id := range ids {
		resume, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		snapshots[id.String()] = resumeExceptLanguages(resume.Fields)
	}
	if backupDir == "" {
		return nil, fmt.Errorf("backup directory is empty; no language write attempted")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Clean(backupDir), "profile-languages-*.json")
	if err != nil {
		return nil, err
	}
	backup := file.Name()
	encodeErr := EncodeJSON(file, Object{"profile": before, "resumes": snapshots})
	closeErr := file.Close()
	if encodeErr != nil {
		return nil, encodeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	current, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	currentFields, err := profileFields(current)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(comparableProfile(fields), comparableProfile(currentFields)) {
		return nil, fmt.Errorf("shared profile changed while preparing language update; no write attempted")
	}
	body, err := c.request(ctx, http.MethodPost, "/shards/applicant/profile/update", nil, Object{"profile": patch}, false)
	if err != nil {
		return nil, fmt.Errorf("%w; backup: %s; use profile languages verify before retrying", err, backup)
	}
	var response Object
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("language update outcome uncertain: %w; backup: %s; do not retry", err, backup)
	}
	if _, err := profileFields(response); err != nil {
		return nil, fmt.Errorf("language update response did not confirm profile.fields; backup: %s; inspect before retrying", backup)
	}
	after, err := c.Profile(ctx)
	if err != nil {
		return nil, fmt.Errorf("language update sent but readback failed: %w; backup: %s; do not retry", err, backup)
	}
	afterFields, err := profileFields(after)
	if err != nil {
		return nil, err
	}
	got, err := languageRows(afterFields["language"])
	if err != nil || !reflect.DeepEqual(got, patch["language"]) {
		return nil, fmt.Errorf("shared language readback differs; backup: %s; inspect before retrying", backup)
	}
	oldProfile, newProfile := comparableProfile(fields), comparableProfile(afterFields)
	delete(oldProfile, "language")
	delete(newProfile, "language")
	if !reflect.DeepEqual(oldProfile, newProfile) {
		return nil, fmt.Errorf("unrequested shared profile fields changed; backup: %s; stopped", backup)
	}
	evidence, err := c.verifyLanguageResumes(ctx, ids, patch["language"].([]any), snapshots)
	if err != nil {
		return nil, fmt.Errorf("language update sent but verification failed: %w; backup: %s; do not retry", err, backup)
	}
	result["applied"], result["verified"], result["backup_path"], result["affected_resumes"] = true, true, backup, evidence
	return result, nil
}
