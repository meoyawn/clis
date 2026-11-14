package service

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type requestedSkillLevel struct {
	Name    string
	LevelID int
}

type selectedSkillLevel struct {
	ID        int
	LevelID   int
	LevelName string
}

func numericID(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, n > 0
	case float64:
		if n <= 0 || n > 1_000_000_000 || math.Trunc(n) != n {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

func NormalizeSkillLevelsPatch(patch Object) (Object, error) {
	if len(patch) != 1 {
		return nil, fmt.Errorf("skill-level patch must contain only levels; no write attempted")
	}
	rows, ok := patch["levels"].([]any)
	if !ok || len(rows) == 0 {
		return nil, fmt.Errorf("levels must be a nonempty array of name/level_id objects; no write attempted")
	}
	out := make([]any, 0, len(rows))
	seen := map[string]bool{}
	for i, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || len(row) != 2 {
			return nil, fmt.Errorf("invalid levels[%d]: expected name and level_id only", i)
		}
		name, _ := row["name"].(string)
		levelID, validID := numericID(row["level_id"])
		if strings.TrimSpace(name) != name || name == "" || seen[name] || !validID || levelID < 8 || levelID > 10 {
			return nil, fmt.Errorf("invalid levels[%d]: unique skill name and level_id 8, 9 or 10 required", i)
		}
		seen[name] = true
		out = append(out, Object{"name": name, "level_id": levelID})
	}
	return Object{"levels": out}, nil
}

func requestedSkillLevels(patch Object) []requestedSkillLevel {
	rows := patch["levels"].([]any)
	out := make([]requestedSkillLevel, 0, len(rows))
	for _, value := range rows {
		row := value.(map[string]any)
		out = append(out, requestedSkillLevel{Name: row["name"].(string), LevelID: row["level_id"].(int)})
	}
	return out
}

func selectedSkill(fields Object, name string) (selectedSkillLevel, bool, error) {
	tags, _ := Editable(fields)["keySkills"].([]any)
	selected := false
	for _, value := range tags {
		if value == name {
			selected = true
			break
		}
	}
	if !selected {
		return selectedSkillLevel{}, false, nil
	}
	rows, _ := fields["resumeApplicantSkills"].([]any)
	for _, value := range rows {
		row, _ := value.(map[string]any)
		if row["category"] != "SKILL" || row["name"] != name {
			continue
		}
		id, valid := numericID(row["id"])
		if !valid {
			return selectedSkillLevel{}, true, fmt.Errorf("skill %q has no valid numeric ID", name)
		}
		result := selectedSkillLevel{ID: id}
		if level, ok := row["level"].(map[string]any); ok {
			result.LevelID, valid = numericID(level["id"])
			if !valid || result.LevelID < 8 || result.LevelID > 10 {
				return selectedSkillLevel{}, true, fmt.Errorf("skill %q has an unsupported level ID", name)
			}
			result.LevelName, _ = level["name"].(string)
		}
		return result, true, nil
	}
	return selectedSkillLevel{}, true, fmt.Errorf("selected skill %q is missing from HH's skill inventory", name)
}

func (c *Client) SkillLevels(ctx context.Context, id ResumeID) (Object, error) {
	resume, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	tags, _ := Editable(resume.Fields)["keySkills"].([]any)
	levels := make([]any, 0, len(tags))
	for _, value := range tags {
		name, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("resume %s has an invalid skill tag", id)
		}
		level, selected, err := selectedSkill(resume.Fields, name)
		if err != nil {
			return nil, fmt.Errorf("resume %s skill %q: %w", id, name, err)
		}
		if !selected {
			return nil, fmt.Errorf("resume %s no longer selects skill %q", id, name)
		}
		var levelID any
		if level.LevelID != 0 {
			levelID = level.LevelID
		}
		levels = append(levels, Object{"name": name, "skill_id": level.ID, "level_id": levelID, "level_name": level.LevelName})
	}
	return Object{"resume_id": id.String(), "view_url": resume.ViewURL, "levels": levels}, nil
}

func skillStateWithoutRequestedLevels(fields Object, names map[string]bool) Object {
	state := isolatedState(fields)
	rows, _ := fields["resumeApplicantSkills"].([]any)
	clean := make([]any, 0, len(rows))
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["category"] != "SKILL" || !names[fmt.Sprint(row["name"])] {
			clean = append(clean, value)
			continue
		}
		copyRow := make(Object, len(row))
		for key, item := range row {
			if key != "level" {
				copyRow[key] = item
			}
		}
		clean = append(clean, copyRow)
	}
	state["resumeApplicantSkills"] = clean
	return state
}

func sameSkillLevelState(before, after Object, names map[string]bool) bool {
	oldState := skillStateWithoutRequestedLevels(before, names)
	newState := skillStateWithoutRequestedLevels(after, names)
	if !sameSkillInventory(oldState["resumeApplicantSkills"], newState["resumeApplicantSkills"], nil) {
		return false
	}
	delete(oldState, "resumeApplicantSkills")
	delete(newState, "resumeApplicantSkills")
	return reflect.DeepEqual(oldState, newState)
}

func printedSkillLevel(text, name string) (int, bool) {
	inside, level := false, 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.Join(strings.Fields(raw), " ")
		if line == "Навыки" {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if line == "Языки" || line == "Обо мне" {
			break
		}
		switch line {
		case "Базовый уровень":
			level = 8
		case "Средний уровень":
			level = 9
		case "Продвинутый уровень":
			level = 10
		case "Уровень не указан":
			level = 0
		default:
			if line == name {
				return level, true
			}
		}
	}
	return 0, false
}

func (c *Client) verifySkillLevels(ctx context.Context, id ResumeID, requested []requestedSkillLevel, before map[string]Object) ([]any, error) {
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, level := range requested {
		names[level.Name] = true
	}
	found, affected := false, []any{}
	for _, currentID := range ids {
		resume, err := c.Get(ctx, currentID)
		if err != nil {
			return nil, err
		}
		if old, ok := before[currentID.String()]; ok && !sameSkillLevelState(old, resume.Fields, names) {
			return nil, fmt.Errorf("unrequested content or proficiency changed in resume %s; stopped", currentID)
		}
		if currentID == id {
			found = true
		}
		visible := []requestedSkillLevel{}
		for _, want := range requested {
			got, selected, err := selectedSkill(resume.Fields, want.Name)
			if err != nil {
				return nil, fmt.Errorf("resume %s: %w", currentID, err)
			}
			if currentID == id && !selected {
				return nil, fmt.Errorf("resume %s no longer selects %q; stopped", id, want.Name)
			}
			if !selected {
				continue
			}
			if got.LevelID != want.LevelID {
				return nil, fmt.Errorf("resume %s skill %q level is %d, expected %d; stopped", currentID, want.Name, got.LevelID, want.LevelID)
			}
			visible = append(visible, want)
		}
		if len(visible) == 0 {
			continue
		}
		view, err := c.View(ctx, currentID)
		if err != nil {
			return nil, err
		}
		for _, want := range visible {
			printed, present := printedSkillLevel(view.Text, want.Name)
			if !present || printed != want.LevelID {
				return nil, fmt.Errorf("resume %s print view does not show %q at level %d; stopped", currentID, want.Name, want.LevelID)
			}
		}
		affected = append(affected, Object{"resume_id": currentID.String(), "view_url": view.URL})
	}
	if !found {
		return nil, fmt.Errorf("selected resume %s is not in the account; stopped", id)
	}
	return affected, nil
}

func (c *Client) VerifySkillLevels(ctx context.Context, id ResumeID, patch Object) (Object, error) {
	patch, err := NormalizeSkillLevelsPatch(patch)
	if err != nil {
		return nil, err
	}
	affected, err := c.verifySkillLevels(ctx, id, requestedSkillLevels(patch), nil)
	if err != nil {
		return nil, err
	}
	return Object{"resume_id": id.String(), "verified": true, "affected_resumes": affected}, nil
}

func (c *Client) UpdateSkillLevels(ctx context.Context, id ResumeID, patch Object, apply bool, backupDir string) (Object, error) {
	patch, err := NormalizeSkillLevelsPatch(patch)
	if err != nil {
		return nil, err
	}
	requested := requestedSkillLevels(patch)
	selected, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	changes, wire := []any{}, []any{}
	for _, want := range requested {
		old, present, err := selectedSkill(selected.Fields, want.Name)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, fmt.Errorf("skill %q is not selected in resume %s; no write attempted", want.Name, id)
		}
		if old.LevelID != want.LevelID {
			changes = append(changes, Object{"name": want.Name, "skill_id": old.ID, "before_level_id": old.LevelID, "after_level_id": want.LevelID})
			wire = append(wire, Object{"category": "SKILL", "skill_id": old.ID, "level_id": want.LevelID})
		}
	}
	result := Object{"resume_id": id.String(), "view_url": selected.ViewURL, "changes": changes, "applied": false}
	if !apply {
		return result, nil
	}
	if len(wire) == 0 {
		return c.VerifySkillLevels(ctx, id, patch)
	}
	if backupDir == "" {
		return nil, fmt.Errorf("backup directory is empty; no skill-level write attempted")
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
	found := false
	for _, currentID := range ids {
		resume, err := c.Get(ctx, currentID)
		if err != nil {
			return nil, err
		}
		before[currentID.String()] = resume.Fields
		if currentID == id {
			found = true
			if !reflect.DeepEqual(isolatedState(selected.Fields), isolatedState(resume.Fields)) {
				return nil, fmt.Errorf("resume %s changed while preparing skill levels; no write attempted", id)
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("resume %s is not in the account; no write attempted", id)
	}
	profileBefore, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Clean(backupDir), "skill-levels-*.json")
	if err != nil {
		return nil, err
	}
	backup := file.Name()
	encodeErr := EncodeJSON(file, Object{"selected_resume": id.String(), "profile": profileBefore, "resumes": before})
	closeErr := file.Close()
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
	if !reflect.DeepEqual(isolatedState(selected.Fields), isolatedState(current.Fields)) {
		return nil, fmt.Errorf("resume %s changed before skill-level write; no write attempted", id)
	}
	if _, err := c.request(ctx, http.MethodPut, "/shards/applicant/key_skills/levels/update", nil, Object{"userSkillLevels": wire}, false); err != nil {
		return nil, fmt.Errorf("skill-level write outcome uncertain: %w; backup: %s; use profile skill-levels verify before retrying", err, backup)
	}
	result["applied"] = true
	affected, err := c.verifySkillLevels(ctx, id, requested, before)
	if err != nil {
		return nil, fmt.Errorf("skill-level write sent but verification failed: %w; backup: %s; do not retry", err, backup)
	}
	profileAfter, err := c.Profile(ctx)
	if err != nil {
		return nil, fmt.Errorf("skill-level write sent but shared-profile readback failed: %w; backup: %s; do not retry", err, backup)
	}
	beforeFields, err := profileFields(profileBefore)
	if err != nil {
		return nil, err
	}
	afterFields, err := profileFields(profileAfter)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(comparableProfile(beforeFields), comparableProfile(afterFields)) {
		return nil, fmt.Errorf("unrequested shared-profile fields changed; backup: %s; stopped", backup)
	}
	sort.Slice(affected, func(i, j int) bool {
		a := affected[i].(map[string]any)["resume_id"].(string)
		b := affected[j].(map[string]any)["resume_id"].(string)
		return a < b
	})
	result["verified"] = true
	result["backup_path"] = backup
	result["affected_resumes"] = affected
	return result, nil
}
