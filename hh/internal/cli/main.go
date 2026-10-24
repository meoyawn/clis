package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	hh "github.com/meoyawn/clis/hh/internal/service"
)

const help = `hh — hh.ru through the signed-in Chrome account (sweetcookie)

Usage: hh [--profile Default] [--base-url https://kazan.hh.ru] COMMAND

  resumes list
      List every resume, including its stable public ID and print-view URL.
  vacancy get ID [--output FILE]
      Read the live vacancy, verify its numeric ID, and show employer, salary,
      requirements and account response state. Accepts an hh.ru vacancy URL.
  vacancy response-options ID [--output FILE]
      Read the native response form/preflight through GET only. Never executes
      a quickResponse POST, selects a default resume, or sends a letter.
  applications list [--output FILE]
      Read all pages of HH's active negotiations (excluding deleted/archive).
      Independently match each topic with its vacancy and public resume hash.
      Includes origin type, applicant-submission flag, employer view flag and
      cover-letter presence. Active invitations remain labelled as such. GET only.
  application get VACANCY --resume HASH [--har FILE] [--output FILE]
      Verify an existing application for this exact vacancy and resume.
      Reports topic/chat IDs and letter presence, even if alreadyApplied=false.
      --har binds a recorded edit_ajax POST/success receipt to the live topic,
      and shows the exact text in that recording. Never replays any request.
      This status command reads letter presence, not its current text. Use
      application letter to read the current letter from the associated chat.
  application letter VACANCY --resume HASH [--har FILE] [--output FILE]
      Read current cover-letter text from the live chat, using the same Chrome
      profile at chatik.hh.ru. Verify chat, applicant, topic, vacancy, public
      resume hash and the message's RESPONSE_LETTER resources. GET only.
      --har additionally compares the recorded send text with live text, allowing
      CRLF/LF normalization. Reports chat canEdit metadata without testing edits.
      This does not send messages, mark them read, or replay a recorded request.
      If the original letter is outside the returned message page, fail explicitly;
      older-message pagination is not yet implemented. Never infer it from another
      chat message. Missing add-letter UI does not establish edit restrictions:
      inspect live can_edit_reported_by_chat; no repeat-edit POST is attempted.
  application prepare VACANCY --resume HASH --text-file FILE [--output FILE]
      Prepare an exact local application package using GET requests only.
      Check live eligibility, selected resume/print view and letter limit.
      Includes destination, salary, letter, wire fields and review_sha256.
      Use --no-letter instead of --text-file only for an explicitly empty letter.
  application submit --draft FILE [--send --approved SHA256] [--output FILE]
      Without --send, show the saved package locally. Sending requires BOTH
      flags, the human's prior approval of the exact displayed destination,
      text and resume, and the checksum of that unchanged review package.
      A checksum is a binding mechanism, not evidence of human approval.
      Fresh preflight rejects changed content, prior applications, restrictions,
      tests and unsupported forms. POST once, then verify the topic/resume
      against an independent vacancy readback. Never send a live test.
  profile get [--output FILE]
      Read the shared account profile. Profile contacts may appear in ALL
      resumes; this read command does not change them.
  profile contacts get [--output FILE]
      Read shared website links and configured messenger accounts.
  profile contacts update --patch FILE [--apply] [--backup-dir DIR]
      Preview or replace the account-wide otherCommunicationMethods list.
      This changes shared website contacts in ALL resumes. A private backup,
      profile readback, each resume's rendered contact list, and unchanged
      resume content/phone/email/messengers are checked after applying.
      Patch: {"otherCommunicationMethods":[{"value":"https://example.com",
      "description":"Website"}]}. Maximum 10 links. [] clears the list.
      This command does not change phone, email, or messenger accounts.
  resume get ID [--output FILE]
      Export the selected resume's current fields as JSON.
  resume view ID [--html FILE]
      Read /resume/ID?print=true, verify the returned ID and emit its actual
      employer-facing text as JSON. Optionally save original print HTML.
  resume update ID --patch FILE [--apply] [--backup-dir DIR]
      Preview a partial JSON update. --apply performs it, makes a private
      backup, rereads the selected resume and its print view, and checks that
      every other resume's editable fields stayed unchanged. No profile-wide
      update endpoints are used. Arrays REPLACE the entire selected field.
  resume verify ID --patch FILE
      Read-only verification of expected fields against both saved API data
      and the actual print view. Use after an uncertain write, never resend
      blindly. Experience content can be verified after native row-ID cloning.
  har inspect FILE
      Offline, redacted endpoint/request-schema summary of a recorded HAR.
  --help
      This help is the source of truth for agents and iterative development.

ID is the 38-character hash from /resume/HASH, or an HTTPS hh.ru resume URL.
The SAME ID selects reads, writes and the employer print view. No default
resume, list index, numeric database ID, "latest", or title-based selection.
N resumes are supported. Changes to one resume must not modify any other.
Shared profile contacts have a separate explicit command, with account scope.
The print view reports actual website links and generated_phone_shortcuts.
WhatsApp/Viber/Telegram phone shortcuts are HH-generated controls for a saved
phone number, even when communicationMethods contains no messenger account.
Their presence does not establish that the human uses those services. HH's
recorded renderer exposes no separate switch to disable these phone shortcuts.
HH requires at least one phone record in the recorded resume schema: sending
phone:[] is rejected. To remove the phone and generated buttons from display,
use hiddenFields:["phones","other_contacts"] on the selected resume. This
also hides its website-contact block. Preserve wanted website URLs elsewhere
in visible prose if the human wants them retained, and verify the print view.
The phone remains stored by HH. Other resumes and account login stay unchanged.
view verified:true confirms page identity/rendered content, not that every
contact or sentence matches the human's wishes. Inspect the returned evidence.

Patch fields supported by the recorded native resume endpoint:
  Single string or scalar array (use exported values for enum/number fields):
    title, skills, email, keySkills, professionalRole, workFormats,
    employmentForms, preferredContact, businessTripReadiness, travelTime,
    educationLevel, lang, photo, portfolio, accessType, hiddenFields,
    setkaAccess, autoHideTime
  Object array (export current fields first to see the exact schema):
    experience, primaryEducation, additionalEducation, elementaryEducation,
    attestationEducation, salary, phone, personalSite, recommendation,
    certificate, proftest
  "skills" is HH's ABOUT-ME prose; "keySkills" is the separate tag list (max 30).
  An empty array clears a field only if its exported _conditions permit it.
  Omitted fields are preserved. Null is rejected. Phone requires at least one row.
  Experience uses companyName, position, startDate (YYYY-MM-01), endDate
  (YYYY-MM-01 or null), description, companyUrl, companyAreaId, and optional id.
  Preserve existing IDs/metadata when editing rows. Never send profile resume
  assignment checkboxes, profile-wide fields, or a different resume's row IDs.
  HH may clone a shared experience row when editing one resume: its row ID can
  change, but the public resume ID stays the same. Re-export before a new edit.
  Decoration (logos, computed intervals, display names) and professionId are
  server-owned and omitted from experience patches and content comparisons.
  Scalar examples: {"skills":"New about-me text"}, {"title":"Android engineer"}.
  Salary example: {"salary":[{"amount":600000,"currency":"RUR"}]}.
  Respect the human's gross/net decision; never invent expectations.

Global options (before COMMAND):
  --profile NAME       Chrome profile directory/name; default Default.
  --base-url URL       HTTPS hh.ru origin; default https://kazan.hh.ru.
  --cookies-file FILE  Explicit cookie JSON through sweetcookie, instead of
                       reading Chrome. Cookies are never printed or persisted.

All network commands take one exclusive local account lock. Run one at a time.
stdout contains successful JSON only. Errors/HTTP diagnostics go to stderr:
method, URL, status, duration, request field names, bounded/redacted response.
No automatic retries. Stop on 429 (honor Retry-After), auth redirects, CAPTCHA,
checkpoints, or restrictions. For an uncertain write, inspect get/view before
retrying. A 200 alone is not proof: update verifies readback and print view.
Print view is the resume content an employer sees; it does not change visibility.
Only application submit with both explicit sending flags can apply to a job.
Show the exact package in chat and wait for human approval before using them.
Other commands never apply. No command solves CAPTCHA or requests SMS codes.

Backups default to $XDG_STATE_HOME/hh/backups (or ~/.local/state/hh/backups).
They contain personal data and use mode 0600. Saved HARs can contain personal
data too: keep them local, omit cookies/tokens and never commit raw recordings.
Use confirmed native requests from HAR to add a missing capability; test the
contract locally, run moon run hh:check, then go install ..
Do tasks through hh. Chrome is only for discovering missing network contracts,
not for working around unsupported commands. Preserve the human's submission
and message-approval rules; tooling support is not authorization to send/apply.
`

// Main runs the command-line application.
func Main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	global := flag.NewFlagSet("hh", flag.ContinueOnError)
	global.SetOutput(os.Stderr)
	profile := global.String("profile", "Default", "Chrome profile")
	baseText := global.String("base-url", "https://kazan.hh.ru", "HH origin")
	cookies := global.String("cookies-file", "", "inline cookie JSON file")
	global.Usage = func() { fmt.Fprint(os.Stdout, help) }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(os.Stdout, help)
		return nil
	}
	for _, a := range args {
		if a == "--help" || a == "-h" {
			fmt.Fprint(os.Stdout, help)
			return nil
		}
	}
	if len(args) == 3 && args[0] == "har" && args[1] == "inspect" {
		return inspectHAR(args[2])
	}
	if len(args) < 2 {
		return fmt.Errorf("invalid command %q: run hh --help", strings.Join(args, " "))
	}
	if args[0] != "resume" && !(args[0] == "application" && (args[1] == "get" || args[1] == "letter" || args[1] == "prepare" || args[1] == "submit")) && !(args[0] == "applications" && args[1] == "list") && !(args[0] == "vacancy" && (args[1] == "get" || args[1] == "response-options")) && !(args[0] == "resumes" && args[1] == "list") && !(args[0] == "profile" && (args[1] == "get" || args[1] == "contacts")) {
		return fmt.Errorf("unknown command %q: run hh --help", strings.Join(args[:2], " "))
	}
	if args[0] == "resume" && args[1] != "get" && args[1] != "view" && args[1] != "update" && args[1] != "verify" {
		return fmt.Errorf("unknown resume command %q: run hh --help", args[1])
	}
	contactsAction := ""
	if args[0] == "profile" && args[1] == "contacts" {
		if len(args) < 3 || (args[2] != "get" && args[2] != "update") {
			return fmt.Errorf("profile contacts requires get or update; run hh --help")
		}
		contactsAction = args[2]
	}
	var id hh.ResumeID
	var vacancyID hh.VacancyID
	var err error
	if args[0] == "vacancy" || args[0] == "application" && (args[1] == "prepare" || args[1] == "get" || args[1] == "letter") {
		if len(args) < 3 {
			return fmt.Errorf("vacancy %s requires an explicit ID", args[1])
		}
		vacancyID, err = hh.NewVacancyIDFromValue(args[2])
		if err != nil {
			return err
		}
	}
	if args[0] == "resume" {
		if len(args) < 3 {
			return fmt.Errorf("resume %s requires an explicit ID", args[1])
		}
		id, err = hh.NewResumeIDFromValue(args[2])
		if err != nil {
			return err
		}
	}
	options := flag.NewFlagSet(args[1], flag.ContinueOnError)
	options.SetOutput(os.Stderr)
	output := options.String("output", "", "JSON output file")
	htmlOutput := options.String("html", "", "print HTML output file")
	patchPath := options.String("patch", "", "JSON patch file")
	apply := options.Bool("apply", false, "apply after verification")
	backupDir := options.String("backup-dir", "", "backup directory")
	resumeText := options.String("resume", "", "explicit resume hash for application")
	textFile := options.String("text-file", "", "exact application letter UTF-8 file")
	noLetter := options.Bool("no-letter", false, "explicitly prepare without a letter")
	draftFile := options.String("draft", "", "saved application package")
	send := options.Bool("send", false, "send only after human approval")
	approved := options.String("approved", "", "approved unchanged package SHA-256")
	harPath := options.String("har", "", "recorded letter success receipt; read only, never replay")
	start := 2
	if args[0] == "resume" || args[0] == "vacancy" || contactsAction != "" || args[0] == "application" && (args[1] == "prepare" || args[1] == "get" || args[1] == "letter") {
		start = 3
	}
	if err := options.Parse(args[start:]); err != nil {
		return err
	}
	if options.NArg() != 0 {
		return fmt.Errorf("unexpected arguments %q", options.Args())
	}
	var application hh.ApplicationDraft
	var letter string
	if args[0] == "application" {
		if args[1] == "prepare" || args[1] == "get" || args[1] == "letter" {
			id, err = hh.NewResumeIDFromValue(*resumeText)
			if err != nil {
				return err
			}
		}
		if args[1] == "prepare" {
			if (*textFile == "") == !*noLetter {
				return fmt.Errorf("choose exactly one of --text-file FILE or --no-letter")
			}
			if *textFile != "" {
				data, err := os.ReadFile(*textFile)
				if err != nil {
					return err
				}
				letter = string(data)
			}
		} else if args[1] == "submit" {
			data, err := os.ReadFile(*draftFile)
			if err != nil {
				return fmt.Errorf("read application draft %q: %w", *draftFile, err)
			}
			if err := json.Unmarshal(data, &application); err != nil {
				return err
			}
			if !*send {
				return outputJSON(*output, application)
			}
			if *approved == "" {
				return fmt.Errorf("--send requires --approved SHA256 after the human approves the exact package in chat")
			}
		}
	}
	var patch hh.Object
	if args[1] == "update" || args[1] == "verify" || contactsAction == "update" {
		if *patchPath == "" {
			return fmt.Errorf("%s requires --patch FILE", strings.Join(args[:start], " "))
		}
		data, err := os.ReadFile(*patchPath)
		if err != nil {
			return fmt.Errorf("read patch %q: %w", *patchPath, err)
		}
		if err := json.Unmarshal(data, &patch); err != nil {
			return fmt.Errorf("decode patch %q: %w", *patchPath, err)
		}
		if contactsAction == "update" {
			patch, err = hh.NormalizeContactsPatch(patch)
		} else {
			patch, err = hh.NormalizePatch(patch)
		}
		if err != nil {
			return err
		}
	}
	base, err := url.Parse(*baseText)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %w", *baseText, err)
	}
	if base.Path != "" && base.Path != "/" || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("invalid base URL %q: use only an origin", *baseText)
	}
	base.Path = ""
	unlock, err := lockAccount()
	if err != nil {
		return err
	}
	defer unlock()
	httpClient, err := hh.BrowserClient(ctx, base, *profile, *cookies)
	if err != nil {
		return err
	}
	client := hh.NewClient(httpClient, base)
	if args[0] == "applications" {
		data, err := client.Applications(ctx)
		if err != nil {
			return err
		}
		return outputJSON(*output, data)
	}
	if args[0] == "application" {
		if args[1] == "get" || args[1] == "letter" {
			data, err := client.Application(ctx, vacancyID, id)
			if err != nil {
				return err
			}
			if args[1] == "letter" {
				chatBase, _ := url.Parse("https://chatik.hh.ru")
				chatHTTP, err := hh.BrowserClient(ctx, chatBase, *profile, *cookies)
				if err != nil {
					return err
				}
				if err := hh.NewClient(chatHTTP, chatBase).ReadApplicationLetter(ctx, data); err != nil {
					return err
				}
			}
			if *harPath != "" {
				if err := hh.VerifyRecordedLetter(data, *harPath); err != nil {
					return err
				}
			}
			return outputJSON(*output, data)
		}
		if args[1] == "prepare" {
			data, err := client.PrepareApplication(ctx, vacancyID, id, letter)
			if err != nil {
				return err
			}
			return outputJSON(*output, data)
		}
		data, err := client.SubmitApplication(ctx, application, *approved)
		if err != nil {
			return err
		}
		return outputJSON(*output, data)
	}
	if args[0] == "vacancy" {
		if args[1] == "get" {
			data, err := client.Vacancy(ctx, vacancyID)
			if err != nil {
				return err
			}
			return outputJSON(*output, data)
		}
		data, err := client.ResponseOptions(ctx, vacancyID)
		if err != nil {
			return err
		}
		return outputJSON(*output, hh.Redact(data))
	}
	if args[0] == "profile" {
		if contactsAction == "get" {
			data, err := client.Contacts(ctx)
			if err != nil {
				return err
			}
			return outputJSON(*output, data)
		}
		if contactsAction == "update" {
			if *backupDir == "" {
				*backupDir = filepath.Join(stateDir(), "backups")
			}
			data, err := client.UpdateContacts(ctx, patch, *apply, *backupDir)
			if err != nil {
				return err
			}
			return outputJSON(*output, data)
		}
		data, err := client.Profile(ctx)
		if err != nil {
			return err
		}
		return outputJSON(*output, hh.Redact(data))
	}
	if args[0] == "resumes" {
		data, err := client.List(ctx)
		if err != nil {
			return err
		}
		ids, err := hh.ResumeIDs(data)
		if err != nil {
			return err
		}
		summaries := []hh.Object{}
		for _, id := range ids {
			resume, err := client.Get(ctx, id)
			if err != nil {
				return err
			}
			fields := hh.Editable(resume.Fields)
			title := ""
			if values, ok := fields["title"].([]any); ok && len(values) > 0 {
				title, _ = values[0].(string)
			}
			summaries = append(summaries, hh.Object{"id": id.String(), "title": title, "view_url": resume.ViewURL, "edit_url": resume.EditURL})
		}
		data = hh.Object{"resumes": summaries, "total": len(summaries)}

		return outputJSON(*output, hh.Redact(data))
	}
	switch args[1] {
	case "get":
		res, err := client.Get(ctx, id)
		if err != nil {
			return err
		}
		return outputJSON(*output, res)
	case "view":
		view, err := client.View(ctx, id)
		if err != nil {
			return err
		}
		if *htmlOutput != "" {
			if err := privateWrite(*htmlOutput, view.HTML); err != nil {
				return err
			}
		}
		return outputJSON(*output, view)
	case "update":
		return update(ctx, client, id, patch, *apply, *backupDir)
	case "verify":
		result, err := client.Verify(ctx, id, patch)
		if err != nil {
			return err
		}
		return outputJSON(*output, result)
	}
	return fmt.Errorf("unhandled command %q", args[1])
}

func outputJSON(path string, value any) error {
	if path == "" {
		return hh.EncodeJSON(os.Stdout, value)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := privateWrite(path, append(data, '\n')); err != nil {
		return err
	}
	return hh.EncodeJSON(os.Stdout, hh.Object{"output": path})
}

func privateWrite(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	return errors.Join(err, closeErr)
}

func stateDir() string {
	if path := os.Getenv("XDG_STATE_HOME"); path != "" {
		return filepath.Join(path, "hh")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "hh")
}

func lockAccount() (func(), error) {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "account.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another hh network command is running; await its completion: %w", err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func update(ctx context.Context, client *hh.Client, id hh.ResumeID, patch hh.Object, apply bool, backupDir string) error {
	if backupDir == "" {
		backupDir = filepath.Join(stateDir(), "backups")
	}
	plan, err := client.Update(ctx, id, patch, apply, backupDir)
	if err != nil {
		return err
	}
	return hh.EncodeJSON(os.Stdout, plan)
}

func inspectHAR(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var document struct {
		Log struct {
			Entries []struct {
				Request struct {
					Method   string
					URL      string
					PostData struct{ Text string }
				}
				Response struct{ Status int }
			}
		}
	}
	if err := json.NewDecoder(io.LimitReader(f, 128<<20)).Decode(&document); err != nil {
		return fmt.Errorf("decode HAR %q: %w", path, err)
	}
	type endpoint struct {
		Method   string   `json:"method"`
		Path     string   `json:"path"`
		Count    int      `json:"count"`
		Fields   []string `json:"request_fields,omitempty"`
		Statuses []int    `json:"statuses"`
	}
	rows := map[string]*endpoint{}
	for _, e := range document.Log.Entries {
		u, err := url.Parse(e.Request.URL)
		if err != nil || !hh.IsHHHost(u.Hostname()) {
			continue
		}
		if strings.HasSuffix(u.Path, ".js") || strings.HasSuffix(u.Path, ".css") {
			continue
		}
		key := e.Request.Method + " " + u.Path
		r := rows[key]
		if r == nil {
			r = &endpoint{Method: e.Request.Method, Path: u.Path}
			rows[key] = r
		}
		r.Count++
		if !hasInt(r.Statuses, e.Response.Status) {
			r.Statuses = append(r.Statuses, e.Response.Status)
		}
		var body hh.Object
		if json.Unmarshal([]byte(e.Request.PostData.Text), &body) == nil {
			for key := range body {
				if !hasString(r.Fields, key) {
					r.Fields = append(r.Fields, key)
				}
			}
		}
	}
	var keys []string
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result []*endpoint
	for _, key := range keys {
		sort.Strings(rows[key].Fields)
		sort.Ints(rows[key].Statuses)
		result = append(result, rows[key])
	}
	return hh.EncodeJSON(os.Stdout, hh.Object{"har": path, "endpoints": result})
}
func hasInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func hasString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
