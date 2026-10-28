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
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/meoyawn/clis/linkedin/internal/service"
	"golang.org/x/net/html"
)

const saveProfileIntro = "com.linkedin.sdui.requests.profile.saveProfileIntroForm"

type headlinePlan struct {
	body   []byte
	fields map[string]any
}

type headlineResult struct {
	ProfileURL     string `json:"profile_url"`
	Before         string `json:"before"`
	After          string `json:"after"`
	Applied        bool   `json:"applied"`
	Verified       bool   `json:"verified"`
	AlreadyPresent bool   `json:"already_present"`
	BackupPath     string `json:"backup_path,omitempty"`
}

func runHeadline(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		_, err := io.WriteString(stdout, headlineHelp)
		return err
	}
	if args[0] != "headline" {
		return fmt.Errorf("unknown profile command %q; expected headline", args[0])
	}
	fs := flag.NewFlagSet("profile headline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	text := fs.String("text", "", "exact replacement headline")
	apply := fs.Bool("apply", false, "apply the explicitly requested headline")
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(stdout, headlineHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 || *text == "" {
		return fmt.Errorf("profile headline requires one profile and --text TEXT")
	}
	ref, err := parseProfile(positionals[0])
	if err != nil {
		return err
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	result, err := client.updateHeadline(ctx, ref, *text, *apply, filepath.Join(store.dir, "backups"))
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func prepareHeadline(document string, owner profileResult, text string) (headlinePlan, error) {
	doc, err := html.Parse(strings.NewReader(document))
	if err != nil {
		return headlinePlan{}, err
	}
	data := readDocumentData(doc)
	form := postEditPlan{states: map[string]map[string]any{}, data: data}
	actions := map[string]map[string]any{}
	conflict := false
	for _, row := range data {
		data.walk(row, func(m map[string]any) {
			if m["$type"] == "proto.sdui.actions.core.ServerRequest" && jsonStringAt(m, "value", "requestId") == saveProfileIntro {
				action, _ := m["value"].(map[string]any)
				encoded, _ := json.Marshal(action)
				actions[string(encoded)] = action
			}
			states, _ := m["modelStates"].([]any)
			for _, item := range states {
				state, _ := item.(map[string]any)
				key := jsonStringAt(state, "key", "key", "value", "id")
				if key == "" {
					continue
				}
				if old := form.states[key]; old != nil && !reflect.DeepEqual(old["value"], state["value"]) {
					conflict = true
				}
				form.states[key] = state
			}
		})
	}
	if conflict || len(actions) != 1 {
		return headlinePlan{}, fmt.Errorf("intro form has conflicting states or %d save actions; no write attempted", len(actions))
	}
	for _, action := range actions {
		form.action = action
	}
	args, _ := form.action["requestedArguments"].(map[string]any)
	payload, _ := args["payload"].(map[string]any)
	if payload["profileId"] != strings.TrimPrefix(owner.ProfileURN, "urn:li:fsd_profile:") || "https://www.linkedin.com/in/"+fmt.Sprint(payload["vanityName"])+"/" != owner.URL {
		return headlinePlan{}, fmt.Errorf("intro form does not bind the signed-in profile; no write attempted")
	}
	for field, key := range map[string]string{"headline": "headlineIntroForm", "initialHeadline": "initialHeadlineIntroForm"} {
		if jsonStringAt(payload, field, "key") != key || jsonStringAt(payload, field, "namespace") != "MemoryNamespace" || jsonStringAt(form.states[key], "value", "stringValue") != owner.Headline {
			return headlinePlan{}, fmt.Errorf("intro %s differs from current profile or its native binding changed; no write attempted", field)
		}
	}
	// Record persisted form fields by their payload names. UI lifecycle bindings
	// are not profile data, and the two headline states are the requested change.
	fields := map[string]any{}
	for name, value := range payload {
		switch name {
		case "headline", "initialHeadline", "hasChanges", "progressIndicator", "userExitedForm", "saveOrDeleteRequestPending", "bodyDisplayBinding", "isRefreshRequiredAfterSave":
			continue
		}
		if jsonStringAt(value, "namespace") == "MemoryNamespace" {
			key := jsonStringAt(value, "key")
			if state := form.states[key]; state != nil {
				fields[name] = form.resolveValue(state["value"], 0)
			} else {
				fields[name] = nil
			}
		} else {
			fields[name] = value
		}
	}
	form.states["headlineIntroForm"]["value"] = map[string]any{"$case": "stringValue", "stringValue": text}
	states, err := form.requestedStates(form.action)
	if err != nil {
		return headlinePlan{}, err
	}
	clientArgs := map[string]any{}
	for key, value := range args {
		clientArgs[key] = value
	}
	clientArgs["states"] = states
	clientArgs["screenId"] = "com.linkedin.sdui.flagshipnav.profile.ProfileEditIntroForm"
	clientArgs["knownTemplateIds"] = []any{}
	body, err := json.Marshal(map[string]any{"requestId": saveProfileIntro, "serverRequest": form.action, "states": states, "requestedArguments": clientArgs})
	return headlinePlan{body: body, fields: fields}, err
}

func (c *linkedinClient) updateHeadline(ctx context.Context, ref profileRef, text string, apply bool, backupDir string) (headlineResult, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) != text || text == "" || strings.ContainsAny(text, "\r\n") || len(utf16.Encode([]rune(text))) > 220 {
		return headlineResult{}, fmt.Errorf("headline must be one line with 1–220 UTF-16 code units and no surrounding whitespace")
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return headlineResult{}, err
	}
	before, err := c.profile(ctx, ref)
	if err != nil {
		return headlineResult{}, err
	}
	if before.ProfileURN != self {
		return headlineResult{}, fmt.Errorf("headline updates only support the signed-in profile; no write attempted")
	}
	result := headlineResult{ProfileURL: before.URL, Before: before.Headline, After: text}
	if before.Headline == text {
		result.Verified, result.AlreadyPresent = true, true
		return result, nil
	}
	introURL := before.URL + "edit/intro/"
	document, err := c.profileDocument(ctx, introURL)
	if err != nil {
		return result, err
	}
	plan, err := prepareHeadline(document, before, text)
	if err != nil {
		return result, err
	}
	if !apply {
		return result, nil
	}
	if backupDir == "" {
		return result, fmt.Errorf("headline backup directory is empty; no write attempted")
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(backupDir, "headline-"+fmt.Sprint(time.Now().UnixNano())+"-*.json")
	if err != nil {
		return result, err
	}
	result.BackupPath = file.Name()
	writeErr := writeJSON(file, map[string]any{"before": before, "intro_fields": plan.fields, "replacement": text, "request": json.RawMessage(plan.body)})
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return result, err
	}
	response, err := c.request(ctx, http.MethodPost, "/flagship-web/rsc-action/actions/server-request?sduiid="+url.QueryEscape(saveProfileIntro), bytes.NewReader(plan.body), http.Header{
		"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"}, "Referer": {introURL},
	})
	if err != nil {
		return result, fmt.Errorf("headline save outcome uncertain; inspect profile before retrying; backup %q: %w", result.BackupPath, err)
	}
	receipt, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	response.Body.Close()
	if readErr != nil {
		return result, fmt.Errorf("read headline receipt; inspect profile before retrying: %w", readErr)
	}
	if err := os.WriteFile(result.BackupPath+".receipt", receipt, 0600); err != nil {
		return result, err
	}
	if err := service.ValidateExperienceResponse(receipt); err != nil {
		return result, fmt.Errorf("headline save has no confirmed completion; inspect profile before retrying: %w", err)
	}
	result.Applied = true
	after, err := c.profile(ctx, ref)
	if err != nil {
		return result, fmt.Errorf("headline save readback failed; do not resend: %w", err)
	}
	if after.Headline != text {
		return result, fmt.Errorf("saved headline is not the requested text; inspect profile and backup %q before retrying", result.BackupPath)
	}
	masked := after.person
	masked.Headline = before.Headline
	if !reflect.DeepEqual(before.person, masked) || before.Summary != after.Summary {
		return result, fmt.Errorf("headline saved but other profile identity/summary changed; inspect backup %q", result.BackupPath)
	}
	afterDocument, err := c.profileDocument(ctx, introURL)
	if err != nil {
		return result, err
	}
	afterPlan, err := prepareHeadline(afterDocument, after, text)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(plan.fields, afterPlan.fields) {
		return result, fmt.Errorf("headline saved but other intro fields changed; inspect backup %q", result.BackupPath)
	}
	publicDocument, err := c.profileDocument(ctx, after.URL)
	if err != nil {
		return result, err
	}
	sections, err := parseProfileDocument(profileDocument{URL: after.URL, HTML: publicDocument}, after.Name)
	if err != nil {
		return result, err
	}
	visible := false
	for _, section := range sections {
		for _, block := range section.Blocks {
			if block.Text == text {
				visible = true
			}
		}
	}
	if !visible {
		return result, fmt.Errorf("saved headline is absent from rendered profile; inspect before retrying")
	}
	result.Verified = true
	if err := os.WriteFile(result.BackupPath+".verified.json", mustJSON(result), 0600); err != nil {
		return result, err
	}
	return result, nil
}

const headlineHelp = `Update the headline on your own LinkedIn profile.

Usage:
  linkedin profile headline <person> --text TEXT [--apply]

Preview is read-only. --apply requires the user's instruction for the exact
headline. Load the fresh native intro form, confirm the signed-in owner and
current headline, and change only the headline state. Retain all other fields.
A private backup, native completion receipt, API/form readback and rendered
profile text verify the result. No automatic retries. If a result is uncertain,
read people profile before trying again. An already-present headline is a no-op.
This does not publish a feed post, send a message, or upload application PDFs.
`
