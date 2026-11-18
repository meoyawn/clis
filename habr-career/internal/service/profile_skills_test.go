package service

import "testing"

func TestPrintedProfessionalSkillsMustMatchTheOrderedForm(t *testing.T) {
	rows := []any{Object{"id": float64(1), "title": "Go"}, Object{"id": float64(2), "title": "Docker"}}
	for _, tc := range []struct {
		line string
		want bool
	}{{"Go • Docker", true}, {"Docker • Go", false}, {"Go", false}} {
		r := &Resume{User: Object{"skills": rows}, Text: "Профессиональные навыки\n" + tc.line + "\nЗнание языков\n"}
		err := verifyProfileSkillNames(r, []string{"", "1", "2"}, []any{"Docker"})
		if (err == nil) != tc.want {
			t.Errorf("%q: %v", tc.line, err)
		}
	}
}
