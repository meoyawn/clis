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

	"github.com/meoyawn/clis/linkedin/internal/service"
	"golang.org/x/net/html"
)

const saveSkill = "com.linkedin.sdui.requests.profile.saveProfileSkillForm"
const skillTypeahead = "PROFILE_ADD_SKILLS_FORM_TYPEAHEAD_REQUEST"
const skillAddScreen = "com.linkedin.sdui.flagshipnav.profile.ProfileSkillAddForm"
const skillsPager = "com.linkedin.sdui.pagers.profile.details.skills"

const profileSkillHelp = `Usage:
  linkedin profile skill <person> --name NAME [--apply]

Use an exact native suggestion name, such as "Apache Kafka", "Kubernetes" or
"Redis". Reads all existing skills and the fresh form. Preview performs only
reads, including a read-only typeahead POST. --apply adds one missing skill,
preserves existing skills, associations, About and intro fields, saves a private
backup and verifies the complete skill inventory. An existing skill is a no-op.
No proficiency level, employer association or followed topic is added. No retries;
inspect the saved receipt and current profile after any uncertain save.
`

func runProfileSkill(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("profile skill", flag.ContinueOnError)
	fs.SetOutput(errOut)
	name := fs.String("name", "", "exact native skill name")
	apply := fs.Bool("apply", false, "add the requested skill")
	args, err := parseFlags(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(out, profileSkillHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(args) != 1 || strings.TrimSpace(*name) != *name {
		return fmt.Errorf("profile skill requires one profile and --name NAME; got %q", *name)
	}
	if err := validateProfileText("skill name", *name, 80); err != nil {
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
	result, err := c.addProfileSkill(ctx, ref, *name, *apply, profileBackupDir(store))
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func skillForm(document string, owner profileResult) (postEditPlan, map[string]any, int, error) {
	form, err := readProfileForm(document)
	if err != nil {
		return form, nil, 0, err
	}
	action, err := profileFormAction(form.data, saveSkill)
	if err != nil {
		return form, nil, 0, err
	}
	if err := checkFormOwner(action, owner); err != nil {
		return form, nil, 0, err
	}
	args, _ := action["requestedArguments"].(map[string]any)
	payload, _ := args["payload"].(map[string]any)
	count, ok := payload["totalSkills"].(float64)
	if payload["isEdit"] != false || !ok || count < 0 || count > 100 || count != float64(int(count)) {
		return form, nil, 0, fmt.Errorf("native add-skill form has invalid totalSkills %v or isEdit %v", payload["totalSkills"], payload["isEdit"])
	}
	return form, action, int(count), nil
}

func skillPageData(data documentData, ownerURL string) (map[string]string, error) {
	skills := map[string]string{}
	prefix := ownerURL + "details/skills/edit/forms/"
	var conflict bool
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			label := jsonStringAt(m, "buttonProps", "aria-label")
			if !strings.HasPrefix(label, "Edit ") || !strings.HasSuffix(label, " skill") {
				return
			}
			name := strings.TrimSuffix(strings.TrimPrefix(label, "Edit "), " skill")
			data.walk(m["triggers"], func(n map[string]any) {
				if n["$type"] != "proto.sdui.actions.core.Navigate" {
					return
				}
				u := jsonStringAt(n, "value", "content", "screen", "url")
				if strings.HasPrefix(u, "/in/") {
					u = "https://www.linkedin.com" + u
				}
				if !strings.HasPrefix(u, prefix) || !digits.MatchString(strings.Trim(strings.TrimPrefix(u, prefix), "/")) {
					conflict = true
					return
				}
				if previous, exists := skills[name]; exists && previous != u {
					conflict = true
				}
				skills[name] = u
			})
		})
	}
	if conflict {
		return nil, fmt.Errorf("native skill list has conflicting skills or owner URLs")
	}
	return skills, nil
}

func (c *linkedinClient) profileSkills(ctx context.Context, owner profileResult, count int) (map[string]string, error) {
	document, err := c.profileDocument(ctx, owner.URL+"details/skills/")
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return nil, err
	}
	data := readDocumentData(doc)
	var pager map[string]any
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] == "proto.sdui.actions.requests.PaginationRequest" && m["pagerId"] == skillsPager && jsonStringAt(m, "requestedArguments", "payload", "filter") == "ProfileSkillCategory_ALL" {
				pager = m
			}
		})
	}
	skills, err := skillPageData(data, owner.URL)
	if err != nil {
		return nil, err
	}
	for len(skills) < count {
		if pager == nil {
			return nil, fmt.Errorf("native skill pagination missing at %d of %d skills", len(skills), count)
		}
		args, _ := pager["requestedArguments"].(map[string]any)
		payload, _ := args["payload"].(map[string]any)
		if payload["profileId"] != strings.TrimPrefix(owner.ProfileURN, "urn:li:fsd_profile:") || payload["vanityName"] != strings.Trim(strings.TrimPrefix(owner.URL, "https://www.linkedin.com/in/"), "/") {
			return nil, fmt.Errorf("native skill pager does not bind the signed-in owner")
		}
		payload["start"] = len(skills)
		clientArgs := map[string]any{}
		for k, v := range args {
			clientArgs[k] = v
		}
		clientArgs["states"], clientArgs["screenId"], clientArgs["knownTemplateIds"] = []any{}, "com.linkedin.sdui.flagshipnav.profile.ProfileSkillDetails", []any{}
		body, err := json.Marshal(map[string]any{"pagerId": skillsPager, "clientArguments": clientArgs, "paginationRequest": pager})
		if err != nil {
			return nil, err
		}
		response, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/pagination?sduiid="+url.QueryEscape(skillsPager), bytes.NewReader(body), http.Header{"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}})
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
		response.Body.Close()
		if readErr != nil || len(raw) > 16<<20 {
			return nil, fmt.Errorf("native skill page unreadable or exceeds 16 MiB: %v", readErr)
		}
		page, err := skillPageData(readFlightData(string(raw)), owner.URL)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 || len(page) > 10 {
			return nil, fmt.Errorf("native skill pagination returned %d items at offset %d", len(page), len(skills))
		}
		for name, u := range page {
			if _, exists := skills[name]; exists {
				return nil, fmt.Errorf("native skill pagination repeated %q at offset %d", name, len(skills))
			}
			skills[name] = u
		}
	}
	if len(skills) != count {
		return nil, fmt.Errorf("native skill inventory has %d items, form reports %d", len(skills), count)
	}
	return skills, nil
}

func skillSuggestion(data documentData, nameKey, idKey, name string) (string, error) {
	ids := map[string]bool{}
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			actions, _ := m["actions"].([]any)
			values := map[string]string{}
			for _, item := range actions {
				action, _ := item.(map[string]any)
				if action["$type"] == "proto.sdui.actions.core.SetState" {
					state, _ := action["value"].(map[string]any)
					values[jsonStringAt(state, "state", "key", "key", "value", "id")] = jsonStringAt(state, "state", "value", "stringValue")
				}
			}
			if values[nameKey] == name && digits.MatchString(values[idKey]) {
				ids[values[idKey]] = true
			}
		})
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("native typeahead returned %d exact matches for skill %q; no write attempted", len(ids), name)
	}
	for id := range ids {
		return id, nil
	}
	return "", fmt.Errorf("native skill %q has no ID", name)
}

func (c *linkedinClient) addProfileSkill(ctx context.Context, ref profileRef, name string, apply bool, backupDir string) (profileFormResult, error) {
	result := profileFormResult{After: map[string]any{"skill": name}}
	self, err := c.mailbox(ctx)
	if err != nil {
		return result, err
	}
	before, err := c.profile(ctx, ref)
	if err != nil {
		return result, err
	}
	if before.ProfileURN != self {
		return result, fmt.Errorf("skill editing only supports the signed-in owner")
	}
	result.ProfileURL = before.URL
	document, err := c.profileDocument(ctx, before.URL+"skills/edit/forms/new/")
	if err != nil {
		return result, err
	}
	form, save, count, err := skillForm(document, before)
	if err != nil {
		return result, err
	}
	previous, err := c.profileSkills(ctx, before, count)
	if err != nil {
		return result, err
	}
	result.Before = map[string]any{"skills": previous}
	if _, exists := previous[name]; exists {
		result.Verified = true
		return result, nil
	}
	typeahead, err := profileFormAction(form.data, skillTypeahead)
	if err != nil {
		return result, err
	}
	nameKey, err := profileFieldKey(save, "skillName")
	if err != nil {
		return result, err
	}
	idKey, err := profileFieldKey(save, "skillId")
	if err != nil {
		return result, err
	}
	if err := setProfileState(form, nameKey, name); err != nil {
		return result, err
	}
	suggestions, err := c.sendProfileAction(ctx, form, typeahead, skillAddScreen)
	if err != nil {
		return result, err
	}
	if err := service.ValidateExperienceResponse(suggestions); err != nil {
		return result, err
	}
	id, err := skillSuggestion(readFlightData(string(suggestions)), nameKey, idKey, name)
	if err != nil {
		return result, err
	}
	if err := setProfileState(form, idKey, id); err != nil {
		return result, err
	}
	if !apply {
		return result, nil
	}
	intro, err := c.profileDocument(ctx, before.URL+"edit/intro/")
	if err != nil {
		return result, err
	}
	introBefore, err := prepareHeadline(intro, before, before.Headline)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(backupDir, "profile-skill-*.json")
	if err != nil {
		return result, err
	}
	result.RecordPath = file.Name()
	writeErr := writeJSON(file, map[string]any{"profile": before, "skills": previous, "form": document, "skill": name, "skill_id": id, "intro_fields": introBefore.fields})
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return result, err
	}
	spec := profileFormSpec{screen: skillAddScreen, initialRequest: saveSkill, saveRequest: saveSkill}
	if err := c.saveProfileForm(ctx, form, save, spec, before, result.RecordPath); err != nil {
		return result, fmt.Errorf("skill save not verified; inspect %q before retrying: %w", result.RecordPath, err)
	}
	result.Applied = true
	after, err := c.profile(ctx, ref)
	if err != nil {
		return result, fmt.Errorf("skill saved; readback failed, do not resend: %w", err)
	}
	if !reflect.DeepEqual(before.person, after.person) || before.Summary != after.Summary {
		return result, fmt.Errorf("profile identity or About changed; inspect %q", result.RecordPath)
	}
	afterDocument, err := c.profileDocument(ctx, before.URL+"skills/edit/forms/new/")
	if err != nil {
		return result, err
	}
	_, _, afterCount, err := skillForm(afterDocument, after)
	if err != nil {
		return result, err
	}
	current, err := c.profileSkills(ctx, after, afterCount)
	if err != nil {
		return result, err
	}
	if current[name] == "" || len(current) != len(previous)+1 {
		return result, fmt.Errorf("saved skill %q did not match inventory; do not resend", name)
	}
	for old, u := range previous {
		if current[old] != u {
			return result, fmt.Errorf("existing skill %q changed; inspect %q", old, result.RecordPath)
		}
	}
	afterIntro, err := c.profileDocument(ctx, after.URL+"edit/intro/")
	if err != nil {
		return result, err
	}
	introAfter, err := prepareHeadline(afterIntro, after, after.Headline)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(introBefore.fields, introAfter.fields) {
		return result, fmt.Errorf("profile intro fields changed; inspect %q", result.RecordPath)
	}
	result.Verified = true
	if err := os.WriteFile(result.RecordPath+".verified.json", mustJSON(result), 0600); err != nil {
		return result, err
	}
	return result, nil
}
