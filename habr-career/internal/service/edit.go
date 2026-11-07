package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"golang.org/x/net/html"
)

func saveBackup(dir, prefix string, value any) (string, error) {
	if dir == "" {
		dir = os.Getenv("XDG_STATE_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			dir = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(dir, "habr-career", "backups")
	}
	path := filepath.Join(dir, prefix+"-"+time.Now().UTC().Format("20060102T150405.000000000")+".json")
	if err := SavePrivateJSON(path, value); err != nil {
		return "", err
	}
	return path, nil
}

func validDescription(text string) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return fmt.Errorf("description must be nonempty valid UTF-8; no write attempted")
	}
	return nil
}

func (c *Client) UpdateApplication(ctx context.Context, vacancyID domain.VacancyID, resumeID domain.ResumeID, letter string, apply bool, backupDir string) (Object, error) {
	if err := validDescription(letter); err != nil {
		return nil, err
	}
	v, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	responseID := number(v.Response["id"])
	if v.UserAlias != resumeID.String() || responseID == "" || object(v.Response["author"])["id"] != resumeID.String() || v.Response["canUpdate"] != true || v.csrf == "" {
		return nil, fmt.Errorf("vacancy %s does not confirm an editable existing response for resume %q; no write attempted", vacancyID.String(), resumeID.String())
	}
	result := Object{"vacancy_id": v.ID, "vacancy_url": v.URL, "company": v.Employer["title"], "title": v.Title, "resume_id": resumeID.String(), "response_id": responseID, "previous_letter": v.Response["message"], "letter": letter, "applied": false}
	if !apply {
		return result, nil
	}
	backup, err := saveBackup(backupDir, "application-"+v.ID+"-"+responseID, result)
	if err != nil {
		return nil, err
	}
	_, err = c.request(ctx, http.MethodPatch, "/api/frontend/vacancies/"+v.ID+"/responses/"+responseID, v.csrf, map[string]string{"body": letter})
	if err != nil {
		return nil, fmt.Errorf("updating response %s may be uncertain: %w; backup %q; inspect application letter, never recreate the response", responseID, err, backup)
	}
	readback, err := c.Application(ctx, vacancyID, resumeID)
	if err != nil {
		return nil, fmt.Errorf("response %s update sent but readback failed: %w; backup %q; do not repeat the update", responseID, err, backup)
	}
	saved, ok := readback["letter"].(string)
	if !ok || readback["response_id"] != responseID || strings.TrimRight(saved, "\r\n") != strings.TrimRight(letter, "\r\n") {
		return nil, fmt.Errorf("response %s update readback does not confirm the same ID and reviewed letter; backup %q; inspect before retrying", responseID, backup)
	}
	result["letter"], result["applied"], result["verified"], result["backup_path"] = saved, true, true, backup
	if saved != letter {
		result["submitted_letter"], result["terminal_newlines_normalized"] = letter, true
	}
	return result, nil
}

func (c *Client) DeleteApplication(ctx context.Context, vacancyID domain.VacancyID, resumeID domain.ResumeID, apply bool, backupDir string) (Object, error) {
	v, err := c.Vacancy(ctx, vacancyID)
	if err != nil {
		return nil, err
	}
	responseID := number(v.Response["id"])
	if v.UserAlias != resumeID.String() || responseID == "" || object(v.Response["author"])["id"] != resumeID.String() || v.Response["canDelete"] != true || v.csrf == "" {
		return nil, fmt.Errorf("vacancy %s does not confirm a deletable existing response for resume %q; no write attempted", vacancyID.String(), resumeID.String())
	}
	result := Object{"vacancy_id": v.ID, "vacancy_url": v.URL, "company": v.Employer["title"], "title": v.Title, "resume_id": resumeID.String(), "response_id": responseID, "previous_letter": v.Response["message"], "applied": false}
	if !apply {
		return result, nil
	}
	backup, err := saveBackup(backupDir, "application-delete-"+v.ID+"-"+responseID, result)
	if err != nil {
		return nil, err
	}
	_, err = c.requestBody(ctx, http.MethodDelete, "/api/frontend/vacancies/"+v.ID+"/responses/"+responseID, v.csrf, nil, "", nil, false)
	if err != nil {
		return nil, fmt.Errorf("deleting response %s may be uncertain: %w; backup %q; inspect application get before any retry", responseID, err, backup)
	}
	readback, err := c.Application(ctx, vacancyID, resumeID)
	if err != nil {
		return nil, fmt.Errorf("response %s delete sent but readback failed: %w; backup %q; do not repeat the delete", responseID, err, backup)
	}
	if readback["submitted"] != false {
		return nil, fmt.Errorf("response %s deletion is not confirmed by the native vacancy readback; backup %q; inspect before retrying", responseID, backup)
	}
	result["applied"], result["deleted"], result["verified"], result["backup_path"] = true, true, true, backup
	return result, nil
}

var experienceLink = regexp.MustCompile(`^/profile/experiences/([1-9][0-9]*)/edit$`)

func (c *Client) ownProfilePage(ctx context.Context, resumeID domain.ResumeID, path string) (*page, error) {
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	if profile["alias"] != resumeID.String() {
		return nil, fmt.Errorf("resume %q is not signed-in account %q; stopped", resumeID.String(), profile["alias"])
	}
	p, err := c.readPage(ctx, path)
	if err != nil {
		return nil, err
	}
	identity := find(p.doc, func(n *html.Node) bool {
		return n.Data == "a" && attr(n, "id") == "top_avatar" && attr(n, "href") == "/"+resumeID.String()
	})
	if identity == nil {
		return nil, fmt.Errorf("profile form %q does not confirm account %q; stopped", path, resumeID.String())
	}
	return p, nil
}

func (c *Client) Experiences(ctx context.Context, resumeID domain.ResumeID) (Object, error) {
	p, err := c.ownProfilePage(ctx, resumeID, "/profile/experiences")
	if err != nil {
		return nil, err
	}
	rows := []any{}
	seen := map[string]bool{}
	for _, link := range findAll(p.doc, func(n *html.Node) bool { return n.Data == "a" }) {
		match := experienceLink.FindStringSubmatch(attr(link, "href"))
		if len(match) != 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		rows = append(rows, Object{"id": match[1], "edit_url": Origin + match[0]})
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no native experience edit links found for %q; inspect the profile HTTP contract", resumeID.String())
	}
	return Object{"resume_id": resumeID.String(), "experiences": rows}, nil
}

type Experience struct {
	ID              string     `json:"id"`
	ResumeID        string     `json:"resume_id"`
	Company         string     `json:"company"`
	Position        string     `json:"position"`
	DescriptionHTML string     `json:"description_html"`
	Metadata        url.Values `json:"metadata"`
	csrf            string
	fields          url.Values
}

var experienceFields = map[string]bool{
	"_method": true, "location": true, "presentTime": true,
	"experience[company_name]": true, "experience[company_id]": true, "experience[city_id]": true,
	"experience[specialization_id]": true, "experience[qualification_id]": true, "experience[position]": true,
	"experience[start_date(1i)]": true, "experience[start_date(2i)]": true, "experience[start_date(3i)]": true,
	"experience[end_date(1i)]": true, "experience[end_date(2i)]": true, "experience[end_date(3i)]": true,
	"experience[description]": true, "experience[experience_skills_ids][]": true,
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func rawText(n *html.Node) string {
	var b strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			b.WriteString(child.Data)
		} else {
			b.WriteString(rawText(child))
		}
	}
	return b.String()
}

func readFormFields(form *html.Node, allowed map[string]bool) (url.Values, error) {
	fields := url.Values{}
	for _, n := range findAll(form, func(n *html.Node) bool { return n.Data == "input" || n.Data == "select" || n.Data == "textarea" }) {
		name, kind := attr(n, "name"), attr(n, "type")
		if name == "" || hasAttr(n, "disabled") || kind == "submit" || kind == "button" || (kind == "radio" || kind == "checkbox") && !hasAttr(n, "checked") {
			continue
		}
		if !allowed[name] {
			return nil, fmt.Errorf("unsupported native form field %q; stopped", name)
		}
		value := attr(n, "value")
		switch n.Data {
		case "textarea":
			value = rawText(n)
		case "select":
			options := findAll(n, func(n *html.Node) bool { return n.Data == "option" && !hasAttr(n, "disabled") })
			if len(options) == 0 || hasAttr(n, "multiple") {
				return nil, fmt.Errorf("unsupported select control %q; stopped", name)
			}
			value = attr(options[0], "value")
			for _, option := range options {
				if hasAttr(option, "selected") {
					value = attr(option, "value")
				}
			}
		default:
			if kind == "checkbox" && !hasAttr(n, "value") {
				value = "on"
			}
		}
		fields.Add(name, value)
	}
	return fields, nil
}

func hydrateExperienceFields(fields url.Values, state Object) error {
	present, presentOK := state["isPresentTime"].(bool)
	end, endOK := state["endDate"]
	skills, skillsOK := state["selectedSkills"].([]any)
	categories, categoriesOK := state["selectedCategories"].([]any)
	if state["fieldName"] != "experience" || state["skillsFieldName"] != "experience[experience_skills_ids][]" || state["categoryFieldName"] != "experience[specialization_id]" || !presentOK || !endOK || !skillsOK || !categoriesOK || len(categories) != 1 || present != (end == nil) {
		return fmt.Errorf("native experience widget state is incomplete or unsupported; no write attempted")
	}
	generated := url.Values{"experience[experience_skills_ids][]": {""}}
	for _, skill := range skills {
		id := number(object(skill)["value"])
		if id == "" {
			return fmt.Errorf("native experience skill has no numeric ID; no write attempted")
		}
		generated.Add("experience[experience_skills_ids][]", id)
	}
	category := number(categories[0])
	current := fields["experience[specialization_id]"]
	if category == "" || len(current) < 1 || len(current) > 2 {
		return fmt.Errorf("native experience specialization controls are incomplete; no write attempted")
	}
	for _, value := range current {
		if value != category {
			return fmt.Errorf("native experience specialization control and widget state disagree; no write attempted")
		}
	}
	// The native widget adds its selected category beside the Rails hidden field.
	fields["experience[specialization_id]"] = []string{category, category}
	for key, value := range map[string]any{"start_date": state["startDate"], "end_date": end} {
		year, month := "", ""
		if key != "end_date" || !present {
			text, ok := value.(string)
			date, err := time.Parse("2006-01-02", text)
			if !ok || err != nil || date.Day() != 1 {
				return fmt.Errorf("native experience %s is not a month-precision date; no write attempted", key)
			}
			year, month = fmt.Sprint(date.Year()), fmt.Sprint(int(date.Month()))
		}
		generated.Set("experience["+key+"(1i)]", year)
		generated.Set("experience["+key+"(2i)]", month)
		generated.Set("experience["+key+"(3i)]", "1")
	}
	if present {
		generated.Set("presentTime", "on")
	} else if len(fields["presentTime"]) != 0 {
		return fmt.Errorf("native experience current-employment control and widget state disagree; no write attempted")
	}
	for key, values := range generated {
		if existing, ok := fields[key]; ok && !reflect.DeepEqual(existing, values) {
			return fmt.Errorf("native experience field %q and widget state disagree; no write attempted", key)
		}
		fields[key] = values
	}
	return nil
}

func (c *Client) Experience(ctx context.Context, resumeID domain.ResumeID, id domain.ExperienceID) (*Experience, error) {
	if _, err := domain.NewExperienceIDFromValue(id.String()); err != nil {
		return nil, err
	}
	path := "/profile/experiences/" + id.String()
	p, err := c.ownProfilePage(ctx, resumeID, path+"/edit")
	if err != nil {
		return nil, err
	}
	form := find(p.doc, func(n *html.Node) bool { return n.Data == "form" && attr(n, "id") == "edit_experience_"+id.String() })
	if form == nil || attr(form, "method") != "post" || attr(form, "data-remote") != "true" || (attr(form, "action") != path && attr(form, "action") != Origin+path) {
		return nil, fmt.Errorf("experience %s edit form identity/action is unsupported; stopped", id.String())
	}
	fields, err := readFormFields(form, experienceFields)
	if err != nil {
		return nil, err
	}
	if err := hydrateExperienceFields(fields, p.state); err != nil {
		return nil, err
	}
	if fields.Get("_method") != "patch" || fields.Get("experience[company_name]") == "" || fields.Get("experience[position]") == "" || len(fields["experience[description]"]) != 1 || p.csrf == "" {
		return nil, fmt.Errorf("experience %s has incomplete native identity/description/CSRF fields; stopped", id.String())
	}
	metadata := url.Values{}
	for key, values := range fields {
		if key != "experience[description]" {
			metadata[key] = append([]string(nil), values...)
		}
	}
	return &Experience{ID: id.String(), ResumeID: resumeID.String(), Company: fields.Get("experience[company_name]"), Position: fields.Get("experience[position]"), DescriptionHTML: fields.Get("experience[description]"), Metadata: metadata, csrf: p.csrf, fields: fields}, nil
}

func profileExceptExperience(r *Resume, company, position string) (Object, error) {
	jobs := []any{}
	companies, ok := object(object(r.Fields)["companies"])["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("profile has no native employment rows for preservation checks; stopped")
	}
	matches := 0
	for _, item := range companies {
		row := object(item)
		positions := []any{}
		entries, ok := row["positions"].([]any)
		if !ok {
			return nil, fmt.Errorf("profile employer %v has no native position rows; stopped", row["title"])
		}
		for _, value := range entries {
			p := object(value)
			message := p["message"]
			if row["title"] == company && p["title"] == position {
				message = nil
				matches++
			}
			positions = append(positions, Object{"title": p["title"], "duration": p["duration"], "message": message})
		}
		jobs = append(jobs, Object{"company": row["title"], "positions": positions})
	}
	if matches != 1 {
		return nil, fmt.Errorf("profile has %d matching employment passages for %q / %q; stopped", matches, company, position)
	}
	return Object{"user": resumeContent(r).User, "about": object(object(r.Fields)["about"])["value"], "jobs": jobs}, nil
}

func (c *Client) VerifyExperience(ctx context.Context, resumeID domain.ResumeID, id domain.ExperienceID, description string) (Object, error) {
	current, err := c.Experience(ctx, resumeID, id)
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(current.DescriptionHTML, "\r\n") != strings.TrimRight(description, "\r\n") {
		return nil, fmt.Errorf("experience %s saved description differs from the requested content; inspect before retrying", id.String())
	}
	view, err := c.Resume(ctx, resumeID)
	if err != nil {
		return nil, err
	}
	printText := strings.Join(strings.Fields(view.Text), " ")
	for _, line := range strings.Split(htmlText(description), "\n") {
		if text := strings.Join(strings.Fields(line), " "); text != "" && !strings.Contains(printText, text) {
			return nil, fmt.Errorf("experience %s description is missing from the native employer print view; stopped", id.String())
		}
	}
	return Object{"resume_id": resumeID.String(), "experience_id": id.String(), "company": current.Company, "position": current.Position, "description_html": current.DescriptionHTML, "view_url": view.ViewURL, "verified": true}, nil
}

func (c *Client) UpdateExperience(ctx context.Context, resumeID domain.ResumeID, id domain.ExperienceID, description string, apply bool, backupDir string) (Object, error) {
	if err := validDescription(description); err != nil {
		return nil, err
	}
	before, err := c.Experience(ctx, resumeID, id)
	if err != nil {
		return nil, err
	}
	result := Object{"resume_id": resumeID.String(), "experience_id": id.String(), "company": before.Company, "position": before.Position, "before": before.DescriptionHTML, "after": description, "applied": false}
	if !apply {
		return result, nil
	}
	resumeBefore, err := c.Resume(ctx, resumeID)
	if err != nil {
		return nil, err
	}
	baseline, err := profileExceptExperience(resumeBefore, before.Company, before.Position)
	if err != nil {
		return nil, err
	}
	backup, err := saveBackup(backupDir, "experience-"+id.String(), Object{"experience": before, "resume": resumeBefore})
	if err != nil {
		return nil, err
	}
	before.fields.Set("experience[description]", description)
	if _, err := c.requestForm(ctx, "/profile/experiences/"+id.String(), before.csrf, before.fields); err != nil {
		return nil, fmt.Errorf("experience %s update may be uncertain: %w; backup %q; use resume verify before retrying", id.String(), err, backup)
	}
	after, err := c.Experience(ctx, resumeID, id)
	if err != nil {
		return nil, fmt.Errorf("experience %s update sent but form readback failed: %w; backup %q; do not repeat", id.String(), err, backup)
	}
	if !reflect.DeepEqual(before.Metadata, after.Metadata) {
		return nil, fmt.Errorf("experience %s non-description metadata changed; backup %q; inspect before retrying", id.String(), backup)
	}
	verified, err := c.VerifyExperience(ctx, resumeID, id, description)
	if err != nil {
		return nil, fmt.Errorf("experience %s update sent but verification failed: %w; backup %q; do not repeat", id.String(), err, backup)
	}
	resumeAfter, err := c.Resume(ctx, resumeID)
	if err != nil {
		return nil, err
	}
	current, err := profileExceptExperience(resumeAfter, before.Company, before.Position)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(baseline, current) {
		return nil, fmt.Errorf("profile fields or another employment passage changed; backup %q; inspect before retrying", backup)
	}
	verified["applied"], verified["backup_path"] = true, backup
	return verified, nil
}
