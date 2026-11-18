package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"golang.org/x/net/html"
)

const profileSkillsField = "user[user_skills_ids][]"

const languageIDField = "user[foreign_languages][][language_id]"
const languageGradeField = "user[foreign_languages][][grade_id]"

var customSpecializationField = regexp.MustCompile(`^user\[custom_specializations\]\[[1-9][0-9]*\]$`)

type specialization struct {
	id        domain.ResumeID
	headline  string
	languages []any
	fields    url.Values
	csrf      string
}

// NormalizeSpecializationPatch limits this shared form to explicitly supported
// edits. All other successful controls are reconstructed from fresh form/SSR data.
func NormalizeSpecializationPatch(patch Object) (Object, error) {
	if len(patch) == 0 {
		return nil, fmt.Errorf("specialization patch must contain headline, languages or add_skills")
	}
	out := Object{}
	for key, value := range patch {
		switch key {
		case "add_skills":
			names, ok := value.([]any)
			if !ok || len(names) == 0 || len(names) > 30 {
				return nil, fmt.Errorf("add_skills requires 1–30 skill names")
			}
			seen := map[string]bool{}
			for _, item := range names {
				name, ok := item.(string)
				if !ok || name == "" || name != strings.TrimSpace(name) || seen[name] {
					return nil, fmt.Errorf("add_skills requires unique, nonempty skill names")
				}
				seen[name] = true
			}
			out[key] = names
		case "headline":
			text, ok := value.(string)
			if !ok || !utf8.ValidString(text) || strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 255 {
				return nil, fmt.Errorf("invalid headline: expected 1–255 UTF-8 characters")
			}
			out[key] = text
		case "languages":
			rows, ok := value.([]any)
			if !ok || len(rows) == 0 {
				return nil, fmt.Errorf("invalid languages: expected a nonempty array of language_id/grade_id objects")
			}
			seen := map[string]bool{}
			clean := make([]any, 0, len(rows))
			for i, value := range rows {
				row := object(value)
				id, grade := number(row["language_id"]), number(row["grade_id"])
				if len(row) != 2 || id == "" || grade == "" || grade > "7" || len(grade) != 1 || seen[id] {
					return nil, fmt.Errorf("invalid languages[%d]: unique positive language_id and grade_id 1–7 required", i)
				}
				seen[id] = true
				clean = append(clean, Object{"language_id": row["language_id"], "grade_id": row["grade_id"]})
			}
			out[key] = clean
		default:
			return nil, fmt.Errorf("unsupported specialization patch field %q; expected headline, languages or add_skills", key)
		}
	}
	return out, nil
}

func (c *Client) specialization(ctx context.Context) (*specialization, error) {
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	id, err := domain.NewResumeIDFromValue(fmt.Sprint(profile["alias"]))
	if err != nil {
		return nil, err
	}
	p, err := c.ownProfilePage(ctx, id, "/profile/specialization")
	if err != nil {
		return nil, err
	}
	form := find(p.doc, func(n *html.Node) bool {
		return n.Data == "form" && (attr(n, "action") == "/profile/specialization" || attr(n, "action") == Origin+"/profile/specialization")
	})
	if form == nil || attr(form, "method") != "post" || attr(form, "data-remote") != "true" || p.csrf == "" {
		return nil, fmt.Errorf("unsupported specialization form/action/CSRF; no write attempted")
	}
	allowed := map[string]bool{
		"_method": true, "user[work_state]": true, "user[salary]": true, "user[currency]": true,
		"salary_recommendation": true, "user[resume_headline]": true, "user[raw_specialization_ids][]": true,
		"user[salary_qualification_id]": true, "user[user_skills_ids][]": true, "user[relocation]": true, "user[remote]": true,
		languageIDField: true, languageGradeField: true,
	}
	for _, input := range findAll(form, func(n *html.Node) bool { return n.Data == "input" }) {
		if name := attr(input, "name"); customSpecializationField.MatchString(name) {
			allowed[name] = true
		}
	}
	fields, err := readFormFields(form, allowed)
	if err != nil {
		return nil, err
	}
	skills, skillsOK := p.state["selectedSkills"].([]any)
	categories, categoriesOK := p.state["selectedCategories"].([]any)
	languages, languagesOK := p.state["selectedLanguages"].([]any)
	if fields.Get("_method") != "patch" || fields.Get("user[resume_headline]") == "" || !skillsOK || !categoriesOK || !languagesOK || p.state["allowSelectGroup"] != false || p.state["skillsFieldName"] != "user[user_skills_ids][]" || p.state["categoryFieldName"] != "user[raw_specialization_ids][]" || len(object(p.state["userCategoriesData"])) != 0 {
		return nil, fmt.Errorf("incomplete or unsupported specialization widget state; no write attempted")
	}
	generated := url.Values{"user[user_skills_ids][]": {""}, "user[raw_specialization_ids][]": {}}
	for _, skill := range skills {
		value := number(object(skill)["value"])
		if value == "" {
			return nil, fmt.Errorf("specialization skill has no numeric ID; no write attempted")
		}
		generated.Add("user[user_skills_ids][]", value)
	}
	for _, category := range categories {
		value := number(category)
		if value == "" {
			return nil, fmt.Errorf("specialization category has no numeric ID; no write attempted")
		}
		generated.Add("user[raw_specialization_ids][]", value)
	}
	for key, values := range generated {
		existing := fields[key]
		if len(existing) != 0 && !reflect.DeepEqual(existing, []string{""}) && !reflect.DeepEqual(existing, values) {
			return nil, fmt.Errorf("specialization field %q disagrees with widget state; no write attempted", key)
		}
		fields[key] = values
	}
	rows := make([]any, 0, len(languages))
	for _, value := range languages {
		row := object(value)
		rows = append(rows, Object{"language_id": row["languageId"], "grade_id": row["gradeId"]})
	}
	validated, err := NormalizeSpecializationPatch(Object{"languages": rows})
	if err != nil {
		return nil, fmt.Errorf("saved specialization languages: %w", err)
	}
	return &specialization{id: id, headline: fields.Get("user[resume_headline]"), languages: validated["languages"].([]any), fields: fields, csrf: p.csrf}, nil
}

func (s *specialization) metadata() url.Values {
	out := url.Values{}
	for key, values := range s.fields {
		out[key] = append([]string(nil), values...)
	}
	out.Del("user[resume_headline]")
	out.Del(languageIDField)
	out.Del(languageGradeField)
	return out
}

func (c *Client) Specialization(ctx context.Context) (Object, error) {
	s, err := c.specialization(ctx)
	if err != nil {
		return nil, err
	}
	return Object{"alias": s.id.String(), "headline": s.headline, "languages": s.languages}, nil
}

func specializationResumeContent(r *Resume) Object {
	user := resumeContent(r).User
	delete(user, "foreignLanguages")
	// The headline is nested alongside its edit URL, not a separate title field.
	delete(user, "specialization")
	fields := object(r.Fields)
	return Object{"user": user, "about": object(fields["about"])["value"], "companies": fields["companies"], "education": fields["education"], "additionalEducation": fields["additionalEducation"]}
}

func sameSpecializationLanguages(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	selections := map[string]string{}
	for _, value := range a {
		row := object(value)
		selections[number(row["language_id"])] = number(row["grade_id"])
	}
	for _, value := range b {
		row := object(value)
		if selections[number(row["language_id"])] != number(row["grade_id"]) {
			return false
		}
	}
	return true
}

func (c *Client) appendProfileSkills(ctx context.Context, fields url.Values, names []any) error {
	for _, requested := range names {
		name := requested.(string)
		body, err := c.request(ctx, http.MethodGet, "/api/frontend/suggestions/skills?term="+url.QueryEscape(name), "", nil)
		if err != nil {
			return err
		}
		var results struct {
			List []struct {
				Title string
				Value int
			}
		}
		if err := json.Unmarshal(body, &results); err != nil {
			return fmt.Errorf("skill suggestions: %w", err)
		}
		id := ""
		for _, skill := range results.List {
			if strings.EqualFold(skill.Title, name) && skill.Value > 0 {
				if id != "" {
					return fmt.Errorf("ambiguous native skill %q", name)
				}
				id = fmt.Sprint(skill.Value)
			}
		}
		if id == "" {
			return fmt.Errorf("no exact native skill %q", name)
		}
		if !slices.Contains(fields[profileSkillsField], id) {
			fields.Add(profileSkillsField, id)
		}
	}
	if len(fields[profileSkillsField]) > 31 {
		return fmt.Errorf("profile can select at most 30 skills")
	}
	return nil
}

func verifyProfileSkillNames(r *Resume, ids []string, requested []any) error {
	rows, _ := r.User["skills"].([]any)
	if len(ids) != len(rows)+1 {
		return fmt.Errorf("public skills differ from specialization form")
	}
	_, section, ok := strings.Cut(r.Text, "Профессиональные навыки\n")
	line, _, _ := strings.Cut(section, "\n")
	titles := make([]string, 0, len(rows))
	for i, row := range rows {
		skill := object(row)
		title, _ := skill["title"].(string)
		if number(skill["id"]) != ids[i+1] || title == "" {
			return fmt.Errorf("public skill %d differs from specialization form", i)
		}
		titles = append(titles, title)
	}
	if !ok || !slices.Equal(titles, strings.Split(line, " • ")) {
		return fmt.Errorf("printed skills differ from public skills")
	}
	for _, item := range requested {
		if !slices.ContainsFunc(titles, func(title string) bool { return strings.EqualFold(title, item.(string)) }) {
			return fmt.Errorf("public skill %q missing", item)
		}
	}
	return nil
}

func (c *Client) VerifySpecialization(ctx context.Context, patch Object) (Object, error) {
	patch, err := NormalizeSpecializationPatch(patch)
	if err != nil {
		return nil, err
	}
	s, err := c.specialization(ctx)
	if err != nil {
		return nil, err
	}
	if headline, ok := patch["headline"]; ok && headline != s.headline {
		return nil, fmt.Errorf("saved headline differs from requested text; inspect before retrying")
	}
	if languages, ok := patch["languages"].([]any); ok && !sameSpecializationLanguages(languages, s.languages) {
		return nil, fmt.Errorf("saved languages differ from requested selections; inspect before retrying")
	}
	r, err := c.Resume(ctx, s.id)
	if err != nil {
		return nil, err
	}
	if names, ok := patch["add_skills"].([]any); ok {
		if err := verifyProfileSkillNames(r, s.fields[profileSkillsField], names); err != nil {
			return nil, err
		}
	}
	if object(r.User["specialization"])["title"] != s.headline || !strings.Contains(r.Text, s.headline) {
		return nil, fmt.Errorf("headline missing from public profile or employer print view")
	}
	rows, ok := r.User["foreignLanguages"].([]any)
	if !ok || len(rows) != len(s.languages) {
		return nil, fmt.Errorf("public profile language selections differ from the saved form")
	}
	expectedTitles := []string{}
	for i, value := range rows {
		row, want := object(value), object(s.languages[i])
		title, _ := row["title"].(string)
		// The print view uses Latin C; Habr's SSR labels use Cyrillic С.
		printTitle := strings.ReplaceAll(title, " С", " C")
		if row["language_id"] != want["language_id"] || row["grade_id"] != want["grade_id"] || title == "" {
			return nil, fmt.Errorf("language %d missing or different in public profile/employer print view", i)
		}
		expectedTitles = append(expectedTitles, printTitle)
	}
	_, languageText, found := strings.Cut(r.Text, "Знание языков\n")
	line, _, _ := strings.Cut(languageText, "\n")
	printedTitles := strings.Split(line, " • ")
	sort.Strings(printedTitles)
	sort.Strings(expectedTitles)
	if !found || !reflect.DeepEqual(printedTitles, expectedTitles) {
		return nil, fmt.Errorf("employer print language list differs: got %v, expected %v", printedTitles, expectedTitles)
	}
	return Object{"alias": s.id.String(), "headline": s.headline, "languages": rows, "view_url": r.ViewURL, "verified": true}, nil
}

func (c *Client) UpdateSpecialization(ctx context.Context, patch Object, apply bool, backupDir string) (Object, error) {
	patch, err := NormalizeSpecializationPatch(patch)
	if err != nil {
		return nil, err
	}
	before, err := c.specialization(ctx)
	if err != nil {
		return nil, err
	}
	want := Object{"headline": before.headline, "languages": before.languages}
	for key, value := range patch {
		want[key] = value
	}
	result := Object{"alias": before.id.String(), "before": Object{"headline": before.headline, "languages": before.languages}, "after": want, "applied": false}
	fields := before.metadata()
	names, skillsRequested := patch["add_skills"].([]any)
	if skillsRequested {
		if err := c.appendProfileSkills(ctx, fields, names); err != nil {
			return nil, err
		}
		result["skill_ids"] = fields[profileSkillsField][1:]
		if len(patch) == 1 && slices.Equal(fields[profileSkillsField], before.fields[profileSkillsField]) {
			return c.VerifySpecialization(ctx, patch)
		}
	}
	if !apply {
		return result, nil
	}
	r, err := c.Resume(ctx, before.id)
	if err != nil {
		return nil, err
	}
	backup, err := saveBackup(backupDir, "specialization", Object{"profile": result["before"], "metadata": before.metadata(), "resume": r})
	if err != nil {
		return nil, err
	}
	// Rails parses [] object rows by field order. url.Values.Encode would group
	// all grade IDs before all language IDs and silently pair the wrong rows.
	fields.Set("user[resume_headline]", want["headline"].(string))
	body := fields.Encode()
	keys := []string{languageIDField, languageGradeField}
	for key := range fields {
		keys = append(keys, key)
	}
	for _, value := range want["languages"].([]any) {
		row := object(value)
		body += "&" + url.Values{languageIDField: {number(row["language_id"])}}.Encode()
		body += "&" + url.Values{languageGradeField: {number(row["grade_id"])}}.Encode()
	}
	if _, err := c.requestBody(ctx, http.MethodPost, "/profile/specialization", before.csrf, strings.NewReader(body), "application/x-www-form-urlencoded; charset=UTF-8", keys, true); err != nil {
		return nil, fmt.Errorf("specialization update may be uncertain: %w; backup %q; use profile specialization verify, never retry blindly", err, backup)
	}
	after, err := c.specialization(ctx)
	if err != nil {
		return nil, fmt.Errorf("update sent but form readback failed: %w; backup %q; do not repeat", err, backup)
	}
	expectedMetadata := before.metadata()
	expectedMetadata[profileSkillsField] = fields[profileSkillsField]
	if before.id != after.id || !reflect.DeepEqual(expectedMetadata, after.metadata()) {
		return nil, fmt.Errorf("unrequested specialization fields changed; backup %q; inspect before retrying", backup)
	}
	verified, err := c.VerifySpecialization(ctx, want)
	if err != nil {
		return nil, fmt.Errorf("update sent but verification failed: %w; backup %q; do not repeat", err, backup)
	}
	current, err := c.Resume(ctx, before.id)
	if err != nil {
		return nil, err
	}
	oldContent, newContent := specializationResumeContent(r), specializationResumeContent(current)
	if skillsRequested {
		delete(object(oldContent["user"]), "skills")
		delete(object(newContent["user"]), "skills")
	}
	if !reflect.DeepEqual(oldContent, newContent) {
		return nil, fmt.Errorf("unrequested profile content changed; backup %q; inspect before retrying", backup)
	}
	verified["applied"], verified["backup_path"] = true, backup
	return verified, nil
}
