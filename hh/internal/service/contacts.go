package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

type ContactLink struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

type PrintContacts struct {
	Email                   []string      `json:"email"`
	Phone                   []string      `json:"phone"`
	Websites                []ContactLink `json:"websites"`
	Messengers              []ContactLink `json:"messengers"`
	GeneratedPhoneShortcuts []string      `json:"generated_phone_shortcuts"`
}

type ProfileContacts struct {
	Scope      string        `json:"scope"`
	Websites   []ContactLink `json:"otherCommunicationMethods"`
	Messengers []any         `json:"communicationMethods"`
}

func profileFields(profile Object) (Object, error) {
	p, _ := profile["profile"].(map[string]any)
	fields, ok := p["fields"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("shared profile response has no profile.fields; stopped")
	}
	return fields, nil
}

// Profile record assignments are sets. HH changes their order between reads,
// as well as the order of derived employer logos. Preserve substantive fields.
func comparableProfile(fields Object) Object {
	var normalize func(any, string) any
	normalize = func(value any, key string) any {
		switch v := value.(type) {
		case map[string]any:
			out := make(Object, len(v))
			for name, child := range v {
				out[name] = normalize(child, name)
			}
			return out
		case []any:
			if key == "experience" {
				v = editableExperience(v)
			}
			out := make([]any, len(v))
			for i, child := range v {
				out[i] = normalize(child, "")
			}
			if key == "resumes" {
				sort.Slice(out, func(i, j int) bool {
					a, _ := json.Marshal(out[i])
					b, _ := json.Marshal(out[j])
					return string(a) < string(b)
				})
			}
			return out
		default:
			return value
		}
	}
	return normalize(fields, "").(map[string]any)
}

func linksFromValue(value any) ([]ContactLink, error) {
	rows, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("otherCommunicationMethods must be an array, got %T", value)
	}
	links := make([]ContactLink, 0, len(rows))
	for i, row := range rows {
		item, _ := row.(map[string]any)
		value, vok := item["value"].(string)
		description, dok := item["description"].(string)
		if !vok || !dok {
			return nil, fmt.Errorf("otherCommunicationMethods[%d] requires string value and description", i)
		}
		links = append(links, ContactLink{Value: value, Description: description})
	}
	return links, nil
}

func contactsFromProfile(profile Object) (*ProfileContacts, error) {
	fields, err := profileFields(profile)
	if err != nil {
		return nil, err
	}
	links, err := linksFromValue(fields["otherCommunicationMethods"])
	if err != nil {
		return nil, err
	}
	messengers, ok := fields["communicationMethods"].([]any)
	if !ok {
		return nil, fmt.Errorf("shared profile has no communicationMethods array; stopped")
	}
	return &ProfileContacts{Scope: "account; inherited by all resumes", Websites: links, Messengers: messengers}, nil
}

func (c *Client) Contacts(ctx context.Context) (*ProfileContacts, error) {
	profile, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	return contactsFromProfile(profile)
}

// NormalizeContactsPatch deliberately accepts only the confirmed shared website
// contract. Phone/email authentication and messenger registration use other flows.
func NormalizeContactsPatch(patch Object) (Object, error) {
	if len(patch) != 1 {
		return nil, fmt.Errorf("profile contacts patch must contain only otherCommunicationMethods; no write attempted")
	}
	links, err := linksFromValue(patch["otherCommunicationMethods"])
	if err != nil {
		return nil, err
	}
	if len(links) > 10 {
		return nil, fmt.Errorf("otherCommunicationMethods has %d links; hh.ru permits at most 10", len(links))
	}
	rows := make([]any, 0, len(links))
	seen := map[string]bool{}
	for i, link := range links {
		for field, value := range map[string]string{"value": link.Value, "description": link.Description} {
			if strings.TrimSpace(value) != value || value == "" || utf8.RuneCountInString(value) > 200 {
				return nil, fmt.Errorf("invalid otherCommunicationMethods[%d].%s: use 1–200 characters without surrounding whitespace", i, field)
			}
		}
		u, err := url.Parse(link.Value)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return nil, fmt.Errorf("invalid otherCommunicationMethods[%d].value: expected an HTTPS website URL without credentials", i)
		}
		if seen[canonicalContactURL(link.Value)] {
			return nil, fmt.Errorf("duplicate otherCommunicationMethods[%d] URL %q", i, link.Value)
		}
		seen[canonicalContactURL(link.Value)] = true
		rows = append(rows, Object{"value": link.Value, "description": link.Description})
	}
	return Object{"otherCommunicationMethods": rows}, nil
}

func canonicalContactURL(value string) string {
	u, err := url.Parse(value)
	if err != nil {
		return value
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

type ContactResumeEvidence struct {
	ID       string        `json:"id"`
	URL      string        `json:"url"`
	Contacts PrintContacts `json:"contacts"`
}

type ContactPlan struct {
	Scope                  string                  `json:"scope"`
	Applied                bool                    `json:"applied"`
	Verified               bool                    `json:"verified"`
	Changes                map[string]Change       `json:"changes"`
	AffectedResumes        []ContactResumeEvidence `json:"affected_resumes"`
	ResumeContentUnchanged bool                    `json:"resume_content_unchanged"`
	BackupPath             string                  `json:"backup_path,omitempty"`
}

func (c *Client) UpdateContacts(ctx context.Context, patch Object, apply bool, backupDir string) (*ContactPlan, error) {
	patch, err := NormalizeContactsPatch(patch)
	if err != nil {
		return nil, err
	}
	before, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	oldContacts, err := contactsFromProfile(before)
	if err != nil {
		return nil, err
	}
	want, _ := linksFromValue(patch["otherCommunicationMethods"])
	plan := &ContactPlan{Scope: "account; changes shared contacts in ALL resumes", Changes: map[string]Change{}, AffectedResumes: []ContactResumeEvidence{}}
	if !reflect.DeepEqual(oldContacts.Websites, want) {
		plan.Changes["otherCommunicationMethods"] = Change{Before: oldContacts.Websites, After: want}
	}
	list, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := ResumeIDs(list)
	if err != nil {
		return nil, err
	}
	snapshots := map[string]Object{}
	for _, id := range ids {
		resume, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		snapshots[id.String()] = isolatedState(resume.Fields)
		view, err := c.View(ctx, id)
		if err != nil {
			return nil, err
		}
		plan.AffectedResumes = append(plan.AffectedResumes, ContactResumeEvidence{ID: id.String(), URL: view.URL, Contacts: view.Contacts})
	}
	if !apply {
		return plan, nil
	}
	if len(plan.Changes) > 0 {
		if backupDir == "" {
			return nil, fmt.Errorf("backup directory is empty; no shared contact write attempted")
		}
		if err := os.MkdirAll(backupDir, 0700); err != nil {
			return nil, err
		}
		file, err := os.CreateTemp(filepath.Clean(backupDir), "profile-contacts-*.json")
		if err != nil {
			return nil, err
		}
		plan.BackupPath = file.Name()
		encodeErr := EncodeJSON(file, Object{"profile": before, "resumes": snapshots, "print_contacts": plan.AffectedResumes})
		closeErr := file.Close()
		if encodeErr != nil {
			return nil, encodeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		current, err := c.Profile(ctx)
		if err != nil {
			return nil, err
		}
		currentFields, err := profileFields(current)
		if err != nil {
			return nil, err
		}
		beforeFields, _ := profileFields(before)
		if !reflect.DeepEqual(comparableProfile(beforeFields), comparableProfile(currentFields)) {
			return nil, fmt.Errorf("shared profile changed while preparing update; rerun preview; no write attempted")
		}
		// The legacy profile adapter uses native arrays and returns profile.fields.
		// No resume hash is sent: this operation is explicitly account-wide.
		body, err := c.request(ctx, http.MethodPost, "/shards/applicant/profile/update", nil, Object{"profile": patch}, false)
		if err != nil {
			return nil, fmt.Errorf("%w; backup: %s", err, plan.BackupPath)
		}
		var response Object
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("decode shared contacts update: %w; inspect before retrying; backup: %s", err, plan.BackupPath)
		}
		if _, err := profileFields(response); err != nil {
			return nil, fmt.Errorf("shared contacts update did not confirm profile.fields: %s; inspect before retrying; backup: %s", boundedResponse(body), plan.BackupPath)
		}
		plan.Applied = true
	}
	after, err := c.Profile(ctx)
	if err != nil {
		return nil, err
	}
	newContacts, err := contactsFromProfile(after)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(newContacts.Websites, want) {
		return nil, fmt.Errorf("shared contacts readback differs from requested websites; inspect before retrying; backup: %s", plan.BackupPath)
	}
	beforeFields, _ := profileFields(before)
	afterFields, _ := profileFields(after)
	beforeUnchanged, afterUnchanged := comparableProfile(beforeFields), comparableProfile(afterFields)
	delete(beforeUnchanged, "otherCommunicationMethods")
	delete(afterUnchanged, "otherCommunicationMethods")
	if !reflect.DeepEqual(beforeUnchanged, afterUnchanged) {
		return nil, fmt.Errorf("unrequested shared profile fields changed; stopped; backup: %s", plan.BackupPath)
	}
	for i, id := range ids {
		resume, err := c.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(snapshots[id.String()], isolatedState(resume.Fields)) {
			return nil, fmt.Errorf("unrequested resume content changed for %s; stopped; backup: %s", id, plan.BackupPath)
		}
		view, err := c.View(ctx, id)
		if err != nil {
			return nil, err
		}
		if !sameContactLinks(view.Contacts.Websites, want) {
			return nil, fmt.Errorf("print view %s contains missing, extra, or mislabeled website contacts: got %v, expected %v; backup: %s", id, view.Contacts.Websites, want, plan.BackupPath)
		}
		beforeContacts := plan.AffectedResumes[i].Contacts
		if !reflect.DeepEqual(beforeContacts.Email, view.Contacts.Email) || !reflect.DeepEqual(beforeContacts.Phone, view.Contacts.Phone) || !reflect.DeepEqual(beforeContacts.Messengers, view.Contacts.Messengers) || !reflect.DeepEqual(beforeContacts.GeneratedPhoneShortcuts, view.Contacts.GeneratedPhoneShortcuts) {
			return nil, fmt.Errorf("unrequested rendered phone, email, or messenger changed for %s; backup: %s", id, plan.BackupPath)
		}
		plan.AffectedResumes[i].Contacts = view.Contacts
	}
	plan.ResumeContentUnchanged = true
	plan.Verified = true
	return plan, nil
}

func sameContactLinks(actual, want []ContactLink) bool {
	normalize := func(links []ContactLink) []ContactLink {
		out := append([]ContactLink{}, links...)
		for i := range out {
			out[i].Value = canonicalContactURL(out[i].Value)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Value == out[j].Value {
				return out[i].Description < out[j].Description
			}
			return out[i].Value < out[j].Value
		})
		return out
	}
	return reflect.DeepEqual(normalize(actual), normalize(want))
}
