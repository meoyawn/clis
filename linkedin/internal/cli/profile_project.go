package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type profileProjectDraft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end,omitempty"`
}

func runProfileProject(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("profile project", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", "", "JSON with name, description, start and optional end (YYYY-MM)")
	apply := fs.Bool("apply", false, "add the requested project")
	args, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(out, profileProjectHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(args) != 1 || *file == "" {
		return fmt.Errorf("profile project requires one profile and --file FILE")
	}
	ref, err := parseProfile(args[0])
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var draft profileProjectDraft
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return fmt.Errorf("decode project draft: %w", err)
	}
	changes, err := draft.fields()
	if err != nil {
		return err
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	result, err := c.addProfileProject(ctx, ref, changes, *apply, profileBackupDir(store))
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func (d profileProjectDraft) fields() (map[string]any, error) {
	if err := validateProfileText("project name", d.Name, 255); err != nil {
		return nil, err
	}
	if err := validateProfileText("project description", d.Description, 2000); err != nil {
		return nil, err
	}
	start, err := time.Parse("2006-01", d.Start)
	if err != nil {
		return nil, fmt.Errorf("project start %q must be YYYY-MM", d.Start)
	}
	date := func(t time.Time) map[string]any {
		return map[string]any{"$type": "proto.sdui.common.Date", "day": float64(0), "month": float64(t.Month()), "year": float64(t.Year())}
	}
	fields := map[string]any{"name": d.Name, "description": d.Description, "startDate": date(start), "currentlyWorking": "Checked", "associateId": ""}
	if d.End != "" {
		end, err := time.Parse("2006-01", d.End)
		if err != nil || end.Before(start) {
			return nil, fmt.Errorf("project end %q must be YYYY-MM and not precede start %q", d.End, d.Start)
		}
		fields["endDate"], fields["currentlyWorking"] = date(end), "Unchecked"
	}
	return fields, nil
}

func projectFormURLs(document, ownerURL string) (map[string]bool, error) {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return nil, err
	}
	urls := map[string]bool{}
	prefix := ownerURL + "details/projects/edit/forms/"
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "a" {
			continue
		}
		href := attr(n, "href")
		if strings.HasPrefix(href, "/in/") {
			href = "https://www.linkedin.com" + href
		}
		if strings.HasPrefix(href, prefix) && digits.MatchString(strings.Trim(strings.TrimPrefix(href, prefix), "/")) {
			urls[href] = true
		}
	}
	return urls, nil
}

func (c *linkedinClient) addProfileProject(ctx context.Context, ref profileRef, changes map[string]any, apply bool, backupDir string) (profileFormResult, error) {
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
		return result, fmt.Errorf("project creation only supports the signed-in owner")
	}
	result.ProfileURL = before.URL
	projectsURL := before.URL + "details/projects/"
	previous, err := c.profileDocument(ctx, projectsURL)
	if err != nil {
		return result, err
	}
	sections, err := parseProfileDocument(profileDocument{URL: projectsURL, HTML: previous}, before.Name)
	if err != nil {
		return result, err
	}
	for _, section := range sections {
		for _, block := range section.Blocks {
			if block.Text == changes["name"] {
				return result, fmt.Errorf("project %q already exists; inspect it instead of creating a duplicate", changes["name"])
			}
		}
	}
	beforeURLs, err := projectFormURLs(previous, before.URL)
	if err != nil {
		return result, err
	}
	document, err := c.profileDocument(ctx, before.URL+"edit/forms/project/new/")
	if err != nil {
		return result, err
	}
	form, err := readProfileForm(document)
	if err != nil {
		return result, err
	}
	action, err := profileFormAction(form.data, uploadProjectMedia)
	if err != nil {
		return result, err
	}
	if err := checkFormOwner(action, before); err != nil {
		return result, err
	}
	prefix := jsonStringAt(action, "requestedArguments", "payload", "formIdPrefix")
	if prefix == "" || profileStateValue(form, prefix+"name") != "" {
		return result, fmt.Errorf("native project form is not a blank creation form")
	}
	for field, value := range changes {
		key := prefix + field
		if field == "startDate" || field == "endDate" {
			key = "ProjectForm" + field
		}
		if err := setProfileState(form, key, value); err != nil {
			return result, err
		}
	}
	if !apply {
		return result, nil
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(backupDir, "profile-project-*.json")
	if err != nil {
		return result, err
	}
	result.RecordPath = file.Name()
	writeErr := writeJSON(file, map[string]any{"profile": before, "projects": previous, "form": document, "changes": changes})
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return result, err
	}
	spec := profileFormSpec{screen: "com.linkedin.sdui.flagshipnav.profile.ProfileProjectAddForm", initialRequest: uploadProjectMedia, saveRequest: saveProject}
	if err := c.saveProfileForm(ctx, form, action, spec, before, result.RecordPath); err != nil {
		return result, fmt.Errorf("project outcome not verified; inspect %q before retrying: %w", result.RecordPath, err)
	}
	result.Applied = true
	after, err := c.profile(ctx, ref)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(before.person, after.person) || before.Summary != after.Summary {
		return result, fmt.Errorf("project saved but profile identity/About changed; inspect %q", result.RecordPath)
	}
	next, err := c.profileDocument(ctx, projectsURL)
	if err != nil {
		return result, err
	}
	afterURLs, err := projectFormURLs(next, after.URL)
	if err != nil {
		return result, err
	}
	var added []string
	for u := range afterURLs {
		if !beforeURLs[u] {
			added = append(added, u)
		}
	}
	if len(added) != 1 || len(afterURLs) != len(beforeURLs)+1 {
		return result, fmt.Errorf("expected exactly one new project; found %d new URLs; inspect before retrying", len(added))
	}
	savedDocument, err := c.profileDocument(ctx, added[0])
	if err != nil {
		return result, err
	}
	saved, err := readProfileForm(savedDocument)
	if err != nil {
		return result, err
	}
	savedAction, err := profileFormAction(saved.data, uploadProjectMedia)
	if err != nil {
		return result, err
	}
	prefix = jsonStringAt(savedAction, "requestedArguments", "payload", "formIdPrefix")
	for field, value := range changes {
		key := prefix + field
		if field == "startDate" || field == "endDate" {
			key = "ProjectForm" + field
		}
		if !reflect.DeepEqual(profileStateValue(saved, key), value) {
			return result, fmt.Errorf("project field %q readback differs; inspect %q before retrying", field, added[0])
		}
	}
	result.Verified = true
	if err := os.WriteFile(result.RecordPath+".verified.json", mustJSON(result), 0600); err != nil {
		return result, err
	}
	return result, nil
}

const profileProjectHelp = `Add a dated project to your own profile.

Usage:
  linkedin profile project <person> --file FILE [--apply]

JSON: name, description, start (YYYY-MM), optional end (YYYY-MM).
Omit end for an ongoing project. No contributors or employment association are
added. A duplicate name is rejected. Preview is read-only; --apply requires the
user's instruction. Preserve existing projects and verify the newly assigned
project through its native edit form. Keep a private backup and receipts.
Never retry an uncertain create without inspecting the project list.
`
