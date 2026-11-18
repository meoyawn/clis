package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

const educationPager = "com.linkedin.sdui.pagers.profile.details.education"
const educationDetails = "com.linkedin.sdui.flagshipnav.profile.ProfileEducationDetails"
const educationScreen = "com.linkedin.sdui.flagshipnav.profile.ProfileEducationDetailsEditForm"
const educationMedia = "com.linkedin.sdui.impl.profile.components.forms.education.media"

const educationCardsHelp = `Usage:
  linkedin profile education-cards <profile> --education-id ID [--file JSON] [--apply] [--save-page NEW_FILE]

Read an owned education entry and its link cards. A JSON plan supplies the school,
description and complete ordered card list. Each card has a title and HTTPS URL.
Matching destinations retain their media IDs. Omitted cards are removed on apply.
Preview resolves the link cards. Apply saves the entry and verifies its fields and cards.
`

type educationCard struct {
	ID    string `json:"existing_id,omitempty"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}
type educationPlan struct {
	School      string          `json:"school"`
	Description string          `json:"description"`
	Cards       []educationCard `json:"cards"`
}
type educationEntry struct {
	form      postEditPlan
	initial   map[string]any
	fields    map[string]any
	prefix    string
	cards     []educationCard
	titleKeys map[string]string
}

func readEducationEntry(document string, owner profileResult, id string) (educationEntry, error) {
	var entry educationEntry
	form, err := readProfileForm(document)
	if err != nil {
		return entry, err
	}
	action, err := profileFormAction(form.data, educationMedia)
	if err != nil {
		return entry, err
	}
	if err := checkFormOwner(action, owner); err != nil {
		return entry, err
	}
	args, _ := action["requestedArguments"].(map[string]any)
	payload, _ := args["payload"].(map[string]any)
	prefix, _ := payload["formIdPrefix"].(string)
	if prefix == "" || payload["entityId"] != id {
		return entry, fmt.Errorf("native form does not bind education %s", id)
	}
	fields := map[string]any{}
	for key := range form.states {
		if name, ok := strings.CutPrefix(key, prefix); ok && !strings.Contains(name, "mediaUiCollection") {
			fields[name] = profileStateValue(form, key)
		}
	}
	if fields["allowProfileEditBroadcasts"] != false || fields["schoolName"] == nil {
		return entry, fmt.Errorf("education form does not confirm its school and disabled notifications")
	}
	entry = educationEntry{form: form, initial: action, fields: fields, prefix: prefix, titleKeys: map[string]string{}}
	media, ok := payload["existingMedia"].([]any)
	if !ok {
		return entry, fmt.Errorf("education form is missing existingMedia")
	}
	for _, item := range media {
		id := jsonStringAt(item, "mediaId")
		key := jsonStringAt(item, "title", "value", "key")
		title, ok := profileStateValue(form, key).(string)
		if !digits.MatchString(id) || !ok || entry.titleKeys[id] != "" {
			return entry, fmt.Errorf("education has an invalid existing card")
		}
		entry.cards = append(entry.cards, educationCard{ID: id, Title: title})
		entry.titleKeys[id] = key
	}
	return entry, nil
}
func (c *linkedinClient) educationPage(ctx context.Context, owner profileResult) ([]byte, error) {
	document, err := c.profileDocument(ctx, owner.URL+"details/education/")
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return nil, err
	}
	pager, err := profileDataObject(readDocumentData(doc), "pagerId", educationPager)
	if err != nil {
		return nil, err
	}
	if err := checkFormOwner(pager, owner); err != nil {
		return nil, err
	}
	args, _ := pager["requestedArguments"].(map[string]any)
	clientArgs := map[string]any{}
	for key, value := range args {
		clientArgs[key] = value
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = []any{}, educationDetails, []any{}
	body := mustJSON(map[string]any{"pagerId": educationPager, "clientArguments": clientArgs, "paginationRequest": pager})
	res, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/pagination?sduiid="+educationPager, bytes.NewReader(body), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}})
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("education page exceeds 16 MiB")
	}
	return data, err
}

// Bind each rendered card's own navigation target and caption to the exact school.
func bindEducationLinks(raw []byte, id string, cards []educationCard) error {
	data := readFlightData(string(raw))
	matched := false
	links := map[string]string{}
	var conflict bool
	data.walk(data["0"], func(row map[string]any) {
		if jsonStringAt(row, "viewTrackingSpecs", "viewName") != "education-lockup-view" {
			return
		}
		target := false
		data.walk(row["children"], func(node map[string]any) {
			if jsonStringAt(node, "requestedArguments", "payload", "educationId") == id {
				target = true
			}
		})
		if !target {
			return
		}
		matched = true
		data.walk(row["children"], func(node map[string]any) {
			if jsonStringAt(node, "viewTrackingSpecs", "viewName") != "education-treasury-thumbnail" {
				return
			}
			var destination, title string
			data.walk(node["children"], func(action map[string]any) {
				if u := jsonStringAt(action, "urlValue", "url"); u != "" {
					if destination != "" && destination != u {
						conflict = true
					}
					destination = u
				}
			})
			data.walk(node["children"], func(n map[string]any) {
				children, _ := n["children"].([]any)
				if len(children) != 1 {
					return
				}
				label, _ := children[0].(string)
				for _, card := range cards {
					if label != "" && label == card.Title {
						title = label
					}
				}
			})
			if title == "" || destination == "" || (links[title] != "" && links[title] != destination) {
				conflict = true
				return
			}
			links[title] = destination
		})
	})
	if !matched || conflict || len(links) != len(cards) {
		return fmt.Errorf("rendered education %s has %d identifiable cards; form has %d", id, len(links), len(cards))
	}
	for i, c := range cards {
		if links[c.Title] == "" {
			return fmt.Errorf("missing education card %q", c.Title)
		}
		cards[i].URL = links[c.Title]
	}
	return nil
}

func sameEducationCards(current, want []educationCard) bool {
	return slices.EqualFunc(current, want, func(a, b educationCard) bool { return a.Title == b.Title && a.URL == b.URL })
}

func runEducationCards(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("profile education-cards", flag.ContinueOnError)
	fs.SetOutput(errOut)
	id := fs.String("education-id", "", "exact education ID")
	file := fs.String("file", "", "education plan")
	apply := fs.Bool("apply", false, "save the education plan")
	previewCapture := fs.String("save-preview", "", "save a private native URL preview for one new card")
	capture := fs.String("save-page", "", "new private rendered education page capture")
	args, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(out, educationCardsHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(args) != 1 || !digits.MatchString(*id) || strings.HasPrefix(*id, "0") || (*apply && *file == "") {
		return fmt.Errorf("one profile and a positive --education-id are required; --apply requires --file")
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
		return fmt.Errorf("education edits require the signed-in owner")
	}
	path := owner.URL + "details/education/edit/forms/" + *id + "/"
	document, err := c.profileDocument(ctx, path)
	if err != nil {
		return err
	}
	entry, err := readEducationEntry(document, owner, *id)
	if err != nil {
		return err
	}
	page, err := c.educationPage(ctx, owner)
	if err != nil {
		return err
	}
	if *capture != "" {
		f, err := os.OpenFile(*capture, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.Write(page)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
	}
	if err := bindEducationLinks(page, *id, entry.cards); err != nil {
		return err
	}
	result := map[string]any{"profile_url": owner.URL, "education_id": *id, "before": entry.fields, "existing_cards": entry.cards}
	if *file == "" {
		return writeJSON(out, result)
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var plan educationPlan
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return err
	}
	if plan.School != entry.fields["schoolName"] || len(plan.Cards) == 0 {
		return fmt.Errorf("the requested school and card list must match the education entry")
	}
	if err := validateProfileText("education description", plan.Description, 1000); err != nil {
		return err
	}
	seenURLs := map[string]bool{}
	for i, card := range plan.Cards {
		if card.ID != "" {
			return fmt.Errorf("plan cards use title and url; existing IDs are matched to the fresh rendered links")
		}
		u, err := url.Parse(card.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || seenURLs[card.URL] {
			return fmt.Errorf("invalid or duplicate card URL %q", card.URL)
		}
		seenURLs[card.URL] = true
		for _, current := range entry.cards {
			if current.URL == card.URL {
				plan.Cards[i].ID = current.ID
			}
		}
	}
	result["after"] = plan
	if sameEducationCards(entry.cards, plan.Cards) && entry.fields["description"] == plan.Description {
		result["verified"] = true
		return writeJSON(out, result)
	}
	if *previewCapture != "" && len(plan.Cards) != 1 {
		return fmt.Errorf("--save-preview requires exactly one card")
	}
	if err := c.prepareEducationCards(ctx, entry, plan, *previewCapture); err != nil {
		return err
	}
	if *apply {
		backup := profileBackupDir(store)
		if err := os.MkdirAll(backup, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(backup, "education-*.json")
		if err != nil {
			return err
		}
		record := f.Name()
		writeErr := writeJSON(f, map[string]any{"profile": owner, "form": document, "existing_cards": entry.cards, "plan": plan})
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
		spec := profileFormSpec{screen: educationScreen, initialRequest: educationMedia, saveRequest: "com.linkedin.sdui.requests.profile.saveProfileEducationForm", entityID: *id, entityIDField: "educationId"}
		if err := c.saveProfileForm(ctx, entry.form, entry.initial, spec, owner, record); err != nil {
			return fmt.Errorf("education update unverified; inspect %q before retrying: %w", record, err)
		}
		currentDoc, err := c.profileDocument(ctx, path)
		if err != nil {
			return err
		}
		current, err := readEducationEntry(currentDoc, owner, *id)
		if err != nil {
			return err
		}
		updatedPage, err := c.educationPage(ctx, owner)
		if err != nil {
			return err
		}
		if err := bindEducationLinks(updatedPage, *id, current.cards); err != nil {
			return fmt.Errorf("saved education: %w; inspect %q", err, record)
		}
		expectedFields := maps.Clone(entry.fields)
		expectedFields["description"] = plan.Description
		if !reflect.DeepEqual(expectedFields, current.fields) || !sameEducationCards(current.cards, plan.Cards) {
			return fmt.Errorf("saved education fields or cards differ from the plan; inspect %q before retrying", record)
		}
		result["verified"], result["applied"], result["record_path"], result["cards"] = true, true, record, current.cards
		if err := os.WriteFile(record+".verified.json", mustJSON(result), 0600); err != nil {
			return err
		}
	}
	return writeJSON(out, result)
}
func (c *linkedinClient) prepareEducationCards(ctx context.Context, entry educationEntry, plan educationPlan, previewCapture string) error {
	retained, ingested, bindings, order := []any{}, []any{}, []any{}, []any{}
	seen := map[string]bool{}
	for _, card := range plan.Cards {
		if err := validateProfileText("card title", card.Title, 200); err != nil {
			return err
		}
		if card.ID != "" {
			if entry.titleKeys[card.ID] == "" || seen[card.ID] {
				return fmt.Errorf("invalid or duplicate existing card %q", card.ID)
			}
			if err := setProfileState(entry.form, entry.titleKeys[card.ID], card.Title); err != nil {
				return err
			}
			seen[card.ID] = true
			retained = append(retained, card.ID)
			order = append(order, card.ID)
			continue
		}
		if !strings.HasPrefix(card.URL, "https://") || seen[card.URL] {
			return fmt.Errorf("invalid or duplicate link %q", card.URL)
		}
		seen[card.URL] = true
		response, err := c.experienceLinkPreview(ctx, entry.form, card.URL)
		if err != nil {
			return err
		}
		if previewCapture != "" {
			f, err := os.OpenFile(previewCapture, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := f.Write(response)
			if err := errors.Join(writeErr, f.Close()); err != nil {
				return err
			}
		}
		link, err := ingestProfileLink(entry.form, entry.initial, response)
		if err != nil {
			return fmt.Errorf("preview %q: %w", card.Title, err)
		}
		if err := setProfileState(entry.form, link.binding+"-mediaTitle", card.Title); err != nil {
			return err
		}
		ingested = append(ingested, link.id)
		bindings = append(bindings, link.id+":"+link.binding)
		order = append(order, link.binding)
	}
	if err := setProfileState(entry.form, entry.prefix+"description", plan.Description); err != nil {
		return err
	}
	return setProfileMedia(entry.form, entry.initial, retained, ingested, bindings, order)
}
