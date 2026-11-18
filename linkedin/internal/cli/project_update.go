package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"strings"

	"golang.org/x/net/html"
)

const projectEditScreen = "com.linkedin.sdui.flagshipnav.profile.ProfileProjectDetailsEditForm"
const projectUpdateHelp = `Usage:
  linkedin profile project-update <profile> --project-id ID --name NAME --description-file FILE [--url URL --card-title TITLE] [--apply]

Edit the description of an existing project and optionally add one native link card.
The exact project name guards the edit. Preserve existing media, dates, skills and
associations. Verify the saved project form and its rendered link before returning.
`

func projectEditFields(form postEditPlan, action map[string]any, owner profileResult, id string) (map[string]any, string, error) {
	if err := checkFormOwner(action, owner); err != nil {
		return nil, "", err
	}
	if jsonStringAt(action, "requestedArguments", "payload", "entityId") != id {
		return nil, "", fmt.Errorf("native media form does not bind project %s", id)
	}
	prefix := jsonStringAt(action, "requestedArguments", "payload", "formIdPrefix")
	if prefix == "" {
		return nil, "", fmt.Errorf("project form has no prefix")
	}
	fields := map[string]any{}
	for key := range form.states {
		suffix, ok := strings.CutPrefix(key, prefix)
		if ok && !strings.Contains(suffix, "mediaUiCollection") {
			fields[suffix] = profileStateValue(form, key)
		}
	}
	for _, key := range []string{"ProjectFormstartDate", "ProjectFormendDate"} {
		fields[key] = profileStateValue(form, key)
	}
	if fields["name"] == nil || fields["description"] == nil {
		return nil, "", fmt.Errorf("missing project identity or description")
	}
	return fields, prefix, nil
}

func (c *linkedinClient) projectCards(ctx context.Context, owner profileResult, id string) ([]profileLink, error) {
	path := owner.URL + "details/projects/"
	doc, err := c.profileDocument(ctx, path)
	if err != nil {
		return nil, err
	}
	return projectMediaLinks(doc, owner.URL, id)
}

// Project thumbnails precede the title in the native list. Identify their row
// by its edit link, rather than associating a card with the last heading.
func projectMediaLinks(document, ownerURL, id string) ([]profileLink, error) {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return nil, err
	}
	prefix := ownerURL + "details/projects/edit/forms/"
	projectID := func(n *html.Node) string {
		if n.Data != "a" {
			return ""
		}
		href := attr(n, "href")
		if strings.HasPrefix(href, "/in/") {
			href = "https://www.linkedin.com" + href
		}
		suffix, ok := strings.CutPrefix(href, prefix)
		value := strings.TrimSuffix(suffix, "/")
		if !ok || !digits.MatchString(value) {
			return ""
		}
		return value
	}
	found := false
	links := []profileLink{}
	for n := range doc.Descendants() {
		if projectID(n) == id {
			found = true
		}
		if n.Data != "a" {
			continue
		}
		title := mediaAnchorTitle(n)
		if title == "" {
			continue
		}
		u, err := url.Parse(attr(n, "href"))
		if err != nil {
			return nil, err
		}
		if u.Host == "www.linkedin.com" && u.Path == "/safety/go/" {
			u, err = url.Parse(u.Query().Get("url"))
			if err != nil {
				return nil, err
			}
		}
		if u.Host == "" || u.User != nil || u.Scheme != "https" && u.Scheme != "http" {
			return nil, fmt.Errorf("unexpected project card destination")
		}
		for parent := n.Parent; parent != nil; parent = parent.Parent {
			ids := map[string]bool{}
			for child := range parent.Descendants() {
				if key := projectID(child); key != "" {
					ids[key] = true
				}
			}
			if len(ids) > 1 {
				return nil, fmt.Errorf("project card %q has ambiguous project identity", title)
			}
			if len(ids) == 1 {
				if ids[id] {
					links = append(links, profileLink{Text: title, URL: u.String()})
				}
				break
			}
		}
	}
	if !found {
		return nil, fmt.Errorf("project %s missing from owner profile", id)
	}
	return links, nil
}

func runProjectUpdate(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("profile project-update", flag.ContinueOnError)
	fs.SetOutput(errOut)
	id := fs.String("project-id", "", "exact project ID")
	name := fs.String("name", "", "exact project name")
	file := fs.String("description-file", "", "new project description")
	link := fs.String("url", "", "external link card")
	title := fs.String("card-title", "", "card caption")
	apply := fs.Bool("apply", false, "save the requested project")
	args, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(out, projectUpdateHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(args) != 1 || !digits.MatchString(*id) || strings.HasPrefix(*id, "0") || *name == "" || *file == "" || (*link == "") != (*title == "") {
		return fmt.Errorf("one profile, exact project ID and name and --description-file are required; supply --url and --card-title together")
	}
	if *link != "" {
		u, err := url.Parse(*link)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil {
			return fmt.Errorf("invalid card URL")
		}
		if err := validateProfileText("card title", *title, 200); err != nil {
			return err
		}
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	description := string(raw)
	if err := validateProfileText("project description", description, 2000); err != nil {
		return err
	}
	ref, err := parseProfile(args[0])
	if err != nil {
		return err
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return err
	}
	owner, err := c.profile(ctx, ref)
	if err != nil {
		return err
	}
	if self != owner.ProfileURN {
		return fmt.Errorf("expected the signed-in project owner")
	}
	path := owner.URL + "details/projects/edit/forms/" + *id + "/"
	read := func() (string, postEditPlan, map[string]any, error) {
		document, err := c.profileDocument(ctx, path)
		if err != nil {
			return "", postEditPlan{}, nil, err
		}
		form, err := readProfileForm(document)
		if err != nil {
			return document, form, nil, err
		}
		action, err := profileFormAction(form.data, uploadProjectMedia)
		return document, form, action, err
	}
	document, form, action, err := read()
	if err != nil {
		return err
	}
	before, prefix, err := projectEditFields(form, action, owner, *id)
	if err != nil {
		return err
	}
	if before["name"] != *name {
		return fmt.Errorf("project name differs from the requested project")
	}
	previous, err := c.projectCards(ctx, owner, *id)
	if err != nil {
		return err
	}
	existingIDs := []any{}
	argsMap, _ := action["requestedArguments"].(map[string]any)
	payload, _ := argsMap["payload"].(map[string]any)
	media, ok := payload["existingMedia"].([]any)
	if !ok || len(previous) != len(media) {
		return fmt.Errorf("project form has %d existing cards, rendered profile has %d", len(media), len(previous))
	}
	for _, item := range media {
		value := jsonStringAt(item, "mediaId")
		if !digits.MatchString(value) {
			return fmt.Errorf("invalid existing project media ID")
		}
		existingIDs = append(existingIDs, value)
	}
	expected := map[profileLink]int{}
	hasCard := false
	for _, card := range previous {
		expected[card]++
		if *link != "" && sameLinkTarget(card.URL, *link) {
			if card.Text != *title {
				return fmt.Errorf("existing card has a different title %q", card.Text)
			}
			hasCard = true
		}
	}
	add := *link != "" && !hasCard
	result := map[string]any{"profile_url": owner.URL, "project_id": *id, "name": *name, "before": before["description"], "description": description, "applied": false}
	if !add && before["description"] == description {
		result["verified"] = true
		return writeJSON(out, result)
	}
	if add {
		r, err := c.experienceLinkPreview(ctx, form, *link)
		if err != nil {
			return err
		}
		if _, err := prepareExperienceLink(form, action, r, existingIDs, *title); err != nil {
			return err
		}
	}
	if err := setProfileState(form, prefix+"description", description); err != nil {
		return err
	}
	if !*apply {
		return writeJSON(out, result)
	}
	dir := profileBackupDir(store)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "project-update-*.json")
	if err != nil {
		return err
	}
	rec := f.Name()
	writeErr := writeJSON(f, map[string]any{"form": document, "description": description, "cards": previous, "url": *link, "title": *title})
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	spec := profileFormSpec{screen: projectEditScreen, initialRequest: uploadProjectMedia, saveRequest: saveProject, entityID: *id, entityIDField: "projectIdForm"}
	if err := c.saveProfileForm(ctx, form, action, spec, owner, rec); err != nil {
		return fmt.Errorf("project update unverified; inspect %q before retrying: %w", rec, err)
	}
	_, saved, saveAction, err := read()
	if err != nil {
		return err
	}
	after, _, err := projectEditFields(saved, saveAction, owner, *id)
	if err != nil {
		return err
	}
	before["description"] = description
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("saved project fields differ; inspect %q before retrying", rec)
	}
	current, err := c.projectCards(ctx, owner, *id)
	if err != nil {
		return err
	}
	for _, card := range current {
		if expected[card] > 0 {
			expected[card]--
			continue
		}
		if add && card.Text == *title && sameLinkTarget(card.URL, *link) {
			add = false
			continue
		}
		return fmt.Errorf("unexpected saved card %q; inspect %q", card.Text, rec)
	}
	if add {
		return fmt.Errorf("requested project card missing; inspect %q", rec)
	}
	for _, count := range expected {
		if count != 0 {
			return fmt.Errorf("previous project card missing; inspect %q", rec)
		}
	}
	result["applied"], result["verified"], result["cards"], result["record_path"] = true, true, current, rec
	return writeJSON(out, result)
}
