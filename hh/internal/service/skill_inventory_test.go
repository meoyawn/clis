package service

import "testing"

func TestNewlySelectedSkillMayEnterSharedInventory(t *testing.T) {
	oldSkill := map[string]any{"name": "Go", "category": "SKILL", "level": 10}
	old := []any{oldSkill}
	changes := expectedSkillChanges(&Resume{Fields: Object{"resumeApplicantSkills": old}}, Object{"keySkills": []any{"Go", "Docker"}}, nil)
	for _, tc := range []struct {
		name, category string
		want           bool
	}{
		{"Docker", "SKILL", true}, {"Other", "SKILL", false}, {"Docker", "LANG", false},
	} {
		after := []any{oldSkill, map[string]any{"name": tc.name, "category": tc.category}}
		if got := sameSkillInventory(old, after, changes); got != tc.want {
			t.Errorf("%s/%s: got %v, want %v", tc.name, tc.category, got, tc.want)
		}
	}
}
