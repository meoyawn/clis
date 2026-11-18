package service

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"golang.org/x/net/html"
)

const personalPath = "/profile/personal"
const aboutField = "user[about]"
const citizenshipField = "user[citizenship_country_ids][]"

func (c *Client) personalForm(ctx context.Context, id domain.ResumeID) (url.Values, error) {
	p, err := c.ownProfilePage(ctx, id, personalPath+"/edit")
	if err != nil {
		return nil, err
	}
	form := find(p.doc, func(n *html.Node) bool {
		return n.Data == "form" && (attr(n, "action") == Origin+personalPath || attr(n, "action") == personalPath)
	})
	if form == nil || attr(form, "method") != "post" || attr(form, "enctype") != "multipart/form-data" || hasAttr(form, "data-remote") {
		return nil, fmt.Errorf("unsupported personal form for %q", id.String())
	}
	fields, err := readFormFields(form, map[string]bool{
		"_method": true, "authenticity_token": true, "user[avatar]": true, "user[login]": true,
		"user[first_name]": true, "user[last_name]": true, "user[middle_name]": true, "user[gender]": true,
		"user[birthday(3i)]": true, "user[birthday(2i)]": true, "user[birthday(1i)]": true,
		"location": true, "user[city_id]": true, citizenshipField: true, aboutField: true, "user[address]": true,
	})
	if err != nil {
		return nil, err
	}
	if fields.Get("user[login]") != id.String() || fields.Get("_method") != "patch" || fields.Get("authenticity_token") == "" || len(fields[aboutField]) != 1 || fields.Get("user[avatar]") != "" {
		return nil, fmt.Errorf("personal form identity or fields incomplete for %q", id.String())
	}
	fields.Del("user[avatar]")
	countries, ok := p.state["selectedCitizenships"].([]any)
	if !ok {
		return nil, fmt.Errorf("personal form missing selected citizenships")
	}
	var selected []string
	for _, row := range countries {
		value := number(object(row)["value"])
		if value == "" {
			return nil, fmt.Errorf("personal form has an unrecognized citizenship selection")
		}
		selected = append(selected, value)
	}
	if current := fields[citizenshipField]; len(current) != 0 && !reflect.DeepEqual(current, selected) {
		return nil, fmt.Errorf("personal form citizenship fields disagree")
	}
	if len(selected) > 0 {
		fields[citizenshipField] = selected
	}
	return fields, nil
}

func aboutMetadata(fields url.Values) url.Values {
	copy := url.Values{}
	for k, values := range fields {
		if k != "authenticity_token" && k != aboutField {
			copy[k] = values
		}
	}
	return copy
}

// The native personal form submits a document navigation, unlike the AJAX experience form.
func (c *Client) postAbout(ctx context.Context, id domain.ResumeID, fields url.Values) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, values := range fields {
		for _, value := range values {
			if err := writer.WriteField(name, value); err != nil {
				return err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, Origin+personalPath, &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Origin", Origin)
	request.Header.Set("Referer", Origin+personalPath+"/edit")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusFound || response.StatusCode == http.StatusSeeOther {
		location := response.Header.Get("Location")
		next, err := request.URL.Parse(location)
		if location != "" && err == nil && next.Scheme == "https" && next.Host == "career.habr.com" && next.User == nil &&
			(next.Path == "/"+id.String() || next.Path == "/profile" || next.Path == personalPath+"/edit" || next.Path == personalPath) {
			return nil
		}
	}
	return &HTTPError{Method: http.MethodPost, URL: Origin + personalPath, Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}
}

func (c *Client) aboutReadback(ctx context.Context, id domain.ResumeID, description string) (url.Values, *Resume, error) {
	fields, err := c.personalForm(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rendered := func(s string) string { return strings.Join(strings.Fields(htmlText(s)), " ") }
	want := rendered(description)
	if want == "" || rendered(fields.Get(aboutField)) != want {
		return nil, nil, fmt.Errorf("personal form does not contain the requested About text and links")
	}
	r, err := c.Resume(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	public, _ := object(object(r.Fields)["about"])["value"].(string)
	if rendered(public) != want || !strings.Contains(strings.Join(strings.Fields(r.Text), " "), want) {
		return nil, nil, fmt.Errorf("biography text or links differ in the public profile or employer print view")
	}
	return fields, r, nil
}

func (c *Client) VerifyAbout(ctx context.Context, id domain.ResumeID, description string) (Object, error) {
	fields, view, err := c.aboutReadback(ctx, id, description)
	if err != nil {
		return nil, err
	}
	return Object{"resume_id": id.String(), "metadata": aboutMetadata(fields), "view_url": view.ViewURL, "verified": true}, nil
}

func otherAboutContent(r *Resume) Object {
	f := object(r.Fields)
	return Object{"user": resumeContent(r).User, "companies": employmentContent(f["companies"]), "education": educationWithoutAlumniCounts(f["education"]), "additionalEducation": f["additionalEducation"]}
}

// employmentContent keeps the applicant's own employment rows. Employer cards
// also carry live Habr counters, such as a company blog's post count.
func employmentContent(section any) any {
	items, ok := object(section)["items"].([]any)
	if !ok {
		return section
	}
	rows := make([]any, len(items))
	for i, item := range items {
		row := object(item)
		rows[i] = Object{"title": row["title"], "positions": row["positions"]}
	}
	return rows
}

func educationWithoutAlumniCounts(section any) any {
	items, ok := object(section)["items"].([]any)
	if !ok {
		return section
	}
	rows := make([]any, len(items))
	for i, item := range items {
		rows[i] = item
		row := object(item)
		if alumni := object(row["users"]); alumni != nil {
			// The link label counts all Habr users who attended this university.
			link := maps.Clone(alumni)
			delete(link, "title")
			saved := maps.Clone(row)
			saved["users"] = link
			rows[i] = saved
		}
	}
	out := maps.Clone(object(section))
	out["items"] = rows
	return out
}

func (c *Client) UpdateAbout(ctx context.Context, id domain.ResumeID, description string, apply bool, backupDir string) (Object, error) {
	if err := validDescription(description); err != nil {
		return nil, err
	}
	fields, err := c.personalForm(ctx, id)
	if err != nil {
		return nil, err
	}
	old := fields.Get(aboutField)
	result := Object{"resume_id": id.String(), "before": old, "after": description, "applied": false}
	if !apply {
		return result, nil
	}
	before, err := c.Resume(ctx, id)
	if err != nil {
		return nil, err
	}
	metadata := aboutMetadata(fields)
	backup, err := saveBackup(backupDir, "about", Object{"description_html": old, "metadata": metadata, "resume": before})
	if err != nil {
		return nil, err
	}
	fields.Set(aboutField, description)
	if err := c.postAbout(ctx, id, fields); err != nil {
		return nil, fmt.Errorf("biography update may be uncertain: %w; use resume verify-about; backup %q", err, backup)
	}
	saved, view, err := c.aboutReadback(ctx, id, description)
	if err != nil {
		return nil, fmt.Errorf("biography update sent: %w; backup %q", err, backup)
	}
	if !reflect.DeepEqual(metadata, aboutMetadata(saved)) {
		return nil, fmt.Errorf("personal form metadata changed; compare resume verify-about metadata with backup %q", backup)
	}
	if !reflect.DeepEqual(otherAboutContent(before), otherAboutContent(view)) {
		return nil, fmt.Errorf("another resume section changed; compare resume view with backup %q", backup)
	}
	result["applied"], result["verified"], result["backup_path"], result["view_url"] = true, true, backup, view.ViewURL
	return result, nil
}
