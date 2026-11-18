package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"
)

const positionLinkScreen = "com.linkedin.sdui.flagshipnav.profile.ProfileAddLinkForm"
const positionLinkPreview = "com.linkedin.sdui.generated.profile.dsl.impl.urlPreview"

type experienceLinkResult struct {
	ProfileURL string           `json:"profile_url"`
	PositionID string           `json:"position_id"`
	URL        string           `json:"url"`
	Before     []map[string]any `json:"before"`
	Title      string           `json:"title"`
	Applied    bool             `json:"applied"`
	Verified   bool             `json:"verified"`
	RecordPath string           `json:"record_path,omitempty"`
}

func runExperienceLink(ctx context.Context, args []string, store authStore, out, stderr io.Writer) error {
	fs := flag.NewFlagSet("experience link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	position := fs.String("position-id", "", "exact existing position ID")
	link := fs.String("url", "", "single external media link")
	appendCard := fs.Bool("append", false, "retain existing media when adding a link")
	title := fs.String("title", "", "display title for the external link card")
	capture := fs.String("save-response", "", "new private preview capture")
	apply := fs.Bool("apply", false, "replace the exact position's media with the requested link")
	positionals, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = io.WriteString(out, experienceLinkHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 || !digits.MatchString(*position) || (*position)[0] == '0' {
		return fmt.Errorf("experience link requires one profile and a positive --position-id; got %q", *position)
	}
	u, err := url.Parse(*link)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid --url %q: expected an absolute HTTP(S) URL", *link)
	}
	ref, err := parseProfile(positionals[0])
	if err != nil {
		return err
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	if *title != "" {
		if err := validateProfileText("card title", *title, 200); err != nil {
			return err
		}
	}
	result, err := c.editExperienceLink(ctx, ref, *position, *link, *title, *capture, *apply, *appendCard, profileBackupDir(store))
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func (c *linkedinClient) editExperienceLink(ctx context.Context, ref profileRef, position, link, title, capture string, apply, appendCard bool, backupDir string) (experienceLinkResult, error) {
	result := experienceLinkResult{PositionID: position, URL: link}
	self, err := c.mailbox(ctx)
	if err != nil {
		return result, err
	}
	owner, err := c.profile(ctx, ref)
	if err != nil {
		return result, err
	}
	if owner.ProfileURN != self {
		return result, fmt.Errorf("experience media editing only supports the signed-in owner")
	}
	result.ProfileURL = owner.URL
	path := "details/experience/edit/forms/" + position + "/"
	document, err := c.profileDocument(ctx, owner.URL+path)
	if err != nil {
		return result, err
	}
	form, err := readProfileForm(document)
	if err != nil {
		return result, err
	}
	initial, err := profileFormAction(form.data, uploadPositionMedia)
	if err != nil {
		return result, err
	}
	if err := checkFormOwner(initial, owner); err != nil {
		return result, err
	}
	if jsonStringAt(initial, "requestedArguments", "payload", "entityId") != position {
		return result, fmt.Errorf("native media form does not bind position %q", position)
	}
	fieldsAction, err := profileFormAction(form.data, "com.linkedin.sdui.requests.profile.deleteProfilePositionForm")
	if err != nil {
		return result, err
	}
	fields := profileFormFields(form, fieldsAction)
	if fields["allowProfileEditBroadcasts"] != false {
		return result, fmt.Errorf("native position form enables network notifications; no write attempted")
	}
	media, _ := initial["requestedArguments"].(map[string]any)["payload"].(map[string]any)["existingMedia"].([]any)
	for _, item := range media {
		m, _ := item.(map[string]any)
		key := jsonStringAt(m, "title", "value", "key")
		result.Before = append(result.Before, map[string]any{"media_id": m["mediaId"], "title": profileStateValue(form, key)})
	}
	var existing []profileLink
	retained := []any{}
	if appendCard {
		profile, _, err := c.fullProfile(ctx, ref)
		if err != nil {
			return result, err
		}
		existing, err = c.allPositionMediaLinks(ctx, profile, position)
		if err != nil {
			return result, err
		}
		if len(existing) != len(media) {
			return result, fmt.Errorf("rendered position media differs from its native form")
		}
		for _, item := range media {
			id := jsonStringAt(item, "mediaId")
			if !digits.MatchString(id) {
				return result, fmt.Errorf("position has an invalid media ID")
			}
			retained = append(retained, id)
		}
		for _, card := range existing {
			if sameLinkTarget(card.URL, link) {
				if title != "" && card.Text != title {
					return result, fmt.Errorf("position already has this URL with title %q", card.Text)
				}
				result.Title, result.Verified = card.Text, true
				return result, nil
			}
		}
	}
	data, err := c.experienceLinkPreview(ctx, form, link)
	if err != nil {
		return result, err
	}
	if capture != "" {
		f, err := os.OpenFile(capture, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return result, err
		}
		_, writeErr := f.Write(data)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			return result, err
		}
	}
	result.Title, err = prepareExperienceLink(form, initial, data, retained, title)
	if err != nil {
		return result, err
	}
	if !apply {
		return result, nil
	}
	intro, err := c.profileDocument(ctx, owner.URL+"edit/intro/")
	if err != nil {
		return result, err
	}
	introBefore, err := prepareHeadline(intro, owner, owner.Headline)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return result, err
	}
	f, err := os.CreateTemp(backupDir, "experience-link-"+fmt.Sprint(time.Now().UnixNano())+"-*.json")
	if err != nil {
		return result, err
	}
	result.RecordPath = f.Name()
	writeErr := writeJSON(f, map[string]any{"profile": owner, "form": document, "url": link, "preview": string(data), "intro_fields": introBefore.fields})
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return result, err
	}
	spec := profileFormSpec{path: path, screen: "com.linkedin.sdui.flagshipnav.profile.ProfilePositionForm", initialRequest: uploadPositionMedia, saveRequest: savePosition, entityID: position}
	if err := c.saveProfileForm(ctx, form, initial, spec, owner, result.RecordPath); err != nil {
		return result, fmt.Errorf("experience link outcome uncertain; inspect %q before retrying: %w", result.RecordPath, err)
	}
	result.Applied = true
	after, _, err := c.fullProfile(ctx, ref)
	if err != nil {
		return result, fmt.Errorf("experience link saved; readback failed, do not resend: %w", err)
	}
	if !reflect.DeepEqual(owner.person, after.person) || owner.Summary != after.Summary {
		return result, fmt.Errorf("experience link saved but other profile content changed; inspect %q", result.RecordPath)
	}
	cards, err := c.allPositionMediaLinks(ctx, after, position)
	if err != nil {
		return result, err
	}
	if err := verifyPositionCards(cards, position, existing, link, title); err != nil {
		return result, err
	}
	afterDocument, err := c.profileDocument(ctx, owner.URL+path)
	if err != nil {
		return result, err
	}
	afterForm, err := readProfileForm(afterDocument)
	if err != nil {
		return result, err
	}
	afterAction, err := profileFormAction(afterForm.data, "com.linkedin.sdui.requests.profile.deleteProfilePositionForm")
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(fields, profileFormFields(afterForm, afterAction)) {
		return result, fmt.Errorf("experience link saved but other position fields changed; inspect %q", result.RecordPath)
	}
	afterIntroDoc, err := c.profileDocument(ctx, owner.URL+"edit/intro/")
	if err != nil {
		return result, err
	}
	afterIntro, err := prepareHeadline(afterIntroDoc, owner, owner.Headline)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(introBefore.fields, afterIntro.fields) {
		return result, fmt.Errorf("experience link saved but profile intro fields changed; inspect %q", result.RecordPath)
	}
	result.Verified = true
	if err := os.WriteFile(result.RecordPath+".verified.json", mustJSON(result), 0600); err != nil {
		return result, err
	}
	return result, nil
}

type ingestedProfileLink struct{ id, binding, title string }

func prepareExperienceLink(form postEditPlan, initial map[string]any, response []byte, retained []any, title string) (string, error) {
	link, err := ingestProfileLink(form, initial, response)
	if err != nil {
		return "", err
	}
	if title != "" {
		if err := setProfileState(form, link.binding+"-mediaTitle", title); err != nil {
			return "", err
		}
	} else {
		title = link.title
	}
	order := append(append([]any{}, retained...), link.binding)
	err = setProfileMedia(form, initial, retained, []any{link.id}, []any{link.id + ":" + link.binding}, order)
	return title, err
}

func ingestProfileLink(form postEditPlan, initial map[string]any, response []byte) (ingestedProfileLink, error) {
	preview, err := profileFormData(readFlightData(string(response)))
	if err != nil {
		return ingestedProfileLink{}, err
	}
	ingestedKey, err := profileFieldKey(initial, "ingestedContentIds")
	if err != nil {
		return ingestedProfileLink{}, err
	}
	mapKey, err := profileFieldKey(initial, "ingestedMediaIdToBindingIdMap")
	if err != nil {
		return ingestedProfileLink{}, err
	}
	additions := map[string]map[string]bool{}
	for _, row := range preview.data {
		preview.data.walk(row, func(m map[string]any) {
			if m["$type"] != "proto.sdui.actions.core.AddToStringList" {
				return
			}
			key := jsonStringAt(m, "value", "key", "key", "value", "id")
			if additions[key] == nil {
				additions[key] = map[string]bool{}
			}
			additions[key][jsonStringAt(m, "value", "newString")] = true
		})
	}
	if len(additions[ingestedKey]) != 1 || len(additions[mapKey]) != 1 {
		return ingestedProfileLink{}, fmt.Errorf("native URL preview did not produce one attachable link; no profile write attempted")
	}
	var id, binding string
	for value := range additions[ingestedKey] {
		id = value
	}
	for value := range additions[mapKey] {
		prefix, suffix, ok := strings.Cut(value, ":")
		if !ok || prefix != id || suffix == "" {
			return ingestedProfileLink{}, fmt.Errorf("native URL preview link binding does not match ingested content %q", id)
		}
		binding = suffix
	}
	title, _ := profileStateValue(preview, binding+"-mediaTitle").(string)
	if title == "" {
		return ingestedProfileLink{}, fmt.Errorf("native URL preview returned an empty title; no profile write attempted")
	}
	for key, state := range preview.states {
		if form.states[key] == nil {
			cloned := map[string]any{}
			for k, value := range state {
				cloned[k] = preview.resolveValue(value, 0)
			}
			form.states[key] = cloned
		}
	}
	return ingestedProfileLink{id: id, binding: binding, title: title}, nil
}

func setProfileMedia(form postEditPlan, initial map[string]any, retained, ingested, bindings, order []any) error {
	lists := map[string][]any{"allMediaIds": retained, "ingestedContentIds": ingested, "ingestedMediaIdToBindingIdMap": bindings}
	for field, values := range lists {
		key, err := profileFieldKey(initial, field)
		if err != nil {
			return err
		}
		form.states[key] = map[string]any{"value": map[string]any{"$case": "stringListValue", "stringListValue": map[string]any{"stringValues": values}}}
	}
	sortedKey, err := profileFieldKey(initial, "sortedMediaIds")
	if err != nil {
		return err
	}
	// The browser derives this expression from the edited media collection.
	// Keep its native type and substitute only that collection's item keys.
	collection := jsonStringAt(form.states[sortedKey], "value", "expression", "expression", "stringListExpression", "expression", "collectionIdsStringList", "collectionId")
	changed := false
	for _, row := range form.data {
		form.data.walk(row, func(m map[string]any) {
			if m["collectionId"] == collection && collection != "" {
				if _, ok := m["initialItems"].([]any); ok {
					items := make([]any, 0, len(order))
					for _, key := range order {
						items = append(items, map[string]any{"key": key})
					}
					m["initialItems"] = items
					changed = true
				}
			}
		})
	}
	if !changed {
		return fmt.Errorf("native profile media collection %q is missing", collection)
	}
	return nil
}

func positionMediaLinks(profile fullProfileResult, position string) ([]profileLink, bool) {
	const editPath = "/details/experience/edit/forms/"
	var links []profileLink
	found := false
	for _, section := range profile.Sections {
		if section.Title != "Experience" {
			continue
		}

		active := false
		for _, block := range section.Blocks {
			for _, candidate := range block.Links {
				if strings.Contains(candidate.URL, editPath) {
					active = strings.HasSuffix(strings.TrimSuffix(candidate.URL, "/"), "/"+position)
					found = found || active
				}
			}
			if !active {
				continue
			}
			for _, candidate := range block.Links {
				if strings.Contains(candidate.URL, editPath) || strings.Contains(candidate.URL, "/skill-associations-details/") {
					continue
				}
				links = append(links, candidate)
			}
		}
	}
	return links, found
}

func sameLinkTarget(actual, requested string) bool {
	actual = strings.TrimSuffix(actual, "/")
	requested = strings.TrimSuffix(requested, "/")
	return actual == requested || strings.HasPrefix(requested, "http://") && actual == "https://"+strings.TrimPrefix(requested, "http://")
}

func verifyPositionCards(links []profileLink, position string, existing []profileLink, link, title string) error {
	if len(links) != len(existing)+1 {
		return fmt.Errorf("saved position %s has %d rendered cards, expected %d", position, len(links), len(existing)+1)
	}
	old := map[profileLink]int{}
	for _, card := range existing {
		old[card]++
	}
	added := 0
	for _, card := range links {
		if old[card] > 0 {
			old[card]--
			continue
		}
		if sameLinkTarget(card.URL, link) && (title == "" || title == card.Text) {
			added++
			continue
		}
		return fmt.Errorf("unexpected saved position card %q at %q", card.Text, card.URL)
	}
	if added != 1 {
		return fmt.Errorf("saved position %s does not show the requested link %q", position, link)
	}
	return nil
}

func profileDataObject(data documentData, field, value string) (map[string]any, error) {
	matches := map[string]map[string]any{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if _, ok := m["requestedArguments"].(map[string]any); !ok {
				return
			}
			if m[field] == value {
				encoded, _ := json.Marshal(m)
				matches[string(encoded)] = m
			}
		})
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("native profile data exposes %d distinct %s=%q objects", len(matches), field, value)
	}
	for _, m := range matches {
		return m, nil
	}
	return nil, fmt.Errorf("native profile object %s=%q is missing", field, value)
}

func (c *linkedinClient) profileDataRequest(ctx context.Context, form postEditPlan, action map[string]any, id, path string, modal bool) ([]byte, error) {
	states, err := form.requestedStates(action)
	if err != nil {
		return nil, err
	}
	args, _ := action["requestedArguments"].(map[string]any)
	clientArgs := map[string]any{}
	for key, value := range args {
		clientArgs[key] = value
	}
	clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = states, positionLinkScreen, []any{}
	body := map[string]any{"clientArguments": clientArgs}
	if modal {
		body["isModal"] = true
	} else {
		body["componentId"] = id
		delete(clientArgs, "requestedStateKeys")
		delete(clientArgs, "$type")
	}
	response, err := c.request(ctx, http.MethodPost, path, bytes.NewReader(mustJSON(body)), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "X-Li-Rsc-Stream": {"true"}})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if len(data) > 16<<20 {
		return nil, fmt.Errorf("native profile %q response exceeds 16 MiB", id)
	}
	return data, err
}

func (c *linkedinClient) experienceLinkPreview(ctx context.Context, form postEditPlan, link string) ([]byte, error) {
	screen, err := profileDataObject(form.data, "screenId", positionLinkScreen)
	if err != nil {
		return nil, err
	}
	data, err := c.profileDataRequest(ctx, form, screen, positionLinkScreen, "/flagship-web/rsc-action/actions/navigation?screenId="+positionLinkScreen+"&sduiid="+positionLinkScreen, true)
	if err != nil {
		return nil, err
	}
	linkForm, err := profileFormData(readFlightData(string(data)))
	if err != nil {
		return nil, err
	}
	preview, err := profileDataObject(linkForm.data, "newComponentId", positionLinkPreview)
	if err != nil {
		return nil, err
	}
	key := jsonStringAt(preview, "requestedArguments", "payload", "componentData", "inputUrl", "value", "key")
	if err := setProfileState(linkForm, key, link); err != nil {
		return nil, err
	}
	return c.profileDataRequest(ctx, linkForm, preview, positionLinkPreview, "/flagship-web/rsc-action/actions/component?componentId="+positionLinkPreview+"&sduiid="+positionLinkPreview, false)
}

const experienceLinkHelp = `Add a native link card to an owned experience entry.

Usage:
  linkedin experience link <profile> --position-id ID --url URL [--append] [--title TITLE] [--apply] [--save-response NEW_FILE]

Preview reads the signed-in owner's exact native form and URL preview.
--append keeps existing cards and skips an already matching card. Without --append
the requested card replaces the entire media list. --title sets its visible caption.
--apply saves
a private backup, keeps network notifications off, and verifies the exact direct
link, remaining position fields and profile intro. No automatic retries.
`
