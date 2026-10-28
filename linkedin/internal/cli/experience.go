package cli

import (
	"bytes"
	"context"
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

	"github.com/meoyawn/clis/linkedin/internal/service"
)

type experienceUpdateResult struct {
	ProfileURL     string `json:"profile_url"`
	PositionID     string `json:"position_id"`
	Before         string `json:"before"`
	After          string `json:"after"`
	Applied        bool   `json:"applied"`
	Verified       bool   `json:"verified"`
	AlreadyPresent bool   `json:"already_present"`
	BackupPath     string `json:"backup_path,omitempty"`
}

func runExperience(ctx context.Context, args []string, store authStore, stdout, stderr io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		_, err := io.WriteString(stdout, experienceHelp)
		return err
	}
	if args[0] != "update" {
		return fmt.Errorf("unknown experience command %q; expected update", args[0])
	}
	fs := flag.NewFlagSet("experience update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harPath := fs.String("har", "", "native experience-save HAR")
	positionID := fs.String("position-id", "", "exact existing position ID")
	textPath := fs.String("description-file", "", "UTF-8 description file")
	title := fs.String("title", "", "replacement position title")
	apply := fs.Bool("apply", false, "apply the explicitly requested description change")
	positionals, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		_, writeErr := io.WriteString(stdout, experienceHelp)
		return writeErr
	}
	if err != nil {
		return err
	}
	if len(positionals) != 1 || *positionID == "" || *textPath == "" {
		return fmt.Errorf("experience update requires one profile plus --position-id and --description-file")
	}
	ref, err := parseProfile(positionals[0])
	if err != nil {
		return err
	}
	if *harPath == "" {
		if !digits.MatchString(*positionID) || strings.HasPrefix(*positionID, "0") {
			return fmt.Errorf("invalid position ID %q", *positionID)
		}
		text, err := os.ReadFile(*textPath)
		if err != nil {
			return err
		}
		if err := validateProfileText("description", string(text), 2000); err != nil {
			return err
		}
		changes := map[string]any{"description": string(text)}
		if *title != "" {
			if err := validateProfileText("title", *title, 100); err != nil {
				return err
			}
			changes["title"] = *title
		}
		client, err := newLinkedinClient(store)
		if err != nil {
			return err
		}
		spec := profileFormSpec{path: "details/experience/edit/forms/" + *positionID + "/", screen: "com.linkedin.sdui.flagshipnav.profile.ProfilePositionForm", initialRequest: uploadPositionMedia, saveRequest: savePosition, fieldRequest: "com.linkedin.sdui.requests.profile.deleteProfilePositionForm", entityID: *positionID}
		result, err := client.editProfileForm(ctx, ref, spec, changes, *apply, profileBackupDir(store))
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if *title != "" {
		return fmt.Errorf("--title uses the fresh native form; omit --har")
	}
	f, err := os.Open(*harPath)
	if err != nil {
		return fmt.Errorf("open experience HAR %q: %w", *harPath, err)
	}
	capture, loadErr := service.LoadExperienceCapture(f, *positionID)
	f.Close()
	if loadErr != nil {
		return loadErr
	}
	text, err := os.ReadFile(*textPath)
	if err != nil {
		return fmt.Errorf("read description file %q: %w", *textPath, err)
	}
	client, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	result, err := client.updateExperience(ctx, ref, capture, string(text), *apply, filepath.Join(store.dir, "backups"))
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func positionDescription(p fullProfileResult, positionID string) (string, error) {
	var matches []string
	for _, section := range p.Sections {
		if section.Title != "Experience" {
			continue
		}
		for i, block := range section.Blocks {
			for _, link := range block.Links {
				u, err := url.Parse(link.URL)
				if err != nil || u.Host != "www.linkedin.com" ||
					strings.TrimSuffix(u.Path, "/") != "/in/"+strings.Trim(strings.TrimPrefix(p.URL, "https://www.linkedin.com/in/"), "/")+"/details/experience/edit/forms/"+positionID {
					continue
				}
				if i+1 >= len(section.Blocks) {
					return "", fmt.Errorf("position %s has no readable description", positionID)
				}
				matches = append(matches, section.Blocks[i+1].Text)
			}
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one own-profile edit link for position %s; found %d", positionID, len(matches))
	}
	return matches[0], nil
}

func sameProfileText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

func (c *linkedinClient) updateExperience(ctx context.Context, ref profileRef, capture service.ExperienceCapture, text string, apply bool, backupDir string) (experienceUpdateResult, error) {
	body, err := capture.PrepareDescription(text)
	if err != nil {
		return experienceUpdateResult{}, err
	}
	self, err := c.mailbox(ctx)
	if err != nil {
		return experienceUpdateResult{}, err
	}
	if self != "urn:li:fsd_profile:"+capture.ProfileID {
		return experienceUpdateResult{}, fmt.Errorf("captured experience owner is not the signed-in profile")
	}
	before, _, err := c.fullProfile(ctx, ref)
	if err != nil {
		return experienceUpdateResult{}, err
	}
	if before.ProfileURN != self || strings.TrimSuffix(before.URL, "/") != "https://www.linkedin.com/in/"+capture.VanityName {
		return experienceUpdateResult{}, fmt.Errorf("experience updates only support the signed-in profile and exact captured owner")
	}
	current, err := positionDescription(before, capture.PositionID)
	if err != nil {
		return experienceUpdateResult{}, err
	}
	result := experienceUpdateResult{ProfileURL: before.URL, PositionID: capture.PositionID, Before: current, After: text}
	if sameProfileText(current, text) {
		result.Verified = true
		result.AlreadyPresent = true
		return result, nil
	}
	if !sameProfileText(current, capture.Description) {
		return experienceUpdateResult{}, fmt.Errorf("position %s changed since the HAR; capture a fresh save before updating", capture.PositionID)
	}
	if !apply {
		return result, nil
	}
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return result, fmt.Errorf("create experience backup directory: %w", err)
	}
	backup, err := os.OpenFile(filepath.Join(backupDir, "experience-"+capture.PositionID+"-"+fmt.Sprint(time.Now().UnixNano())+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, fmt.Errorf("create experience backup: %w", err)
	}
	result.BackupPath = backup.Name()
	backupErr := writeJSON(backup, before)
	closeErr := backup.Close()
	if err := errors.Join(backupErr, closeErr); err != nil {
		return result, fmt.Errorf("save experience backup: %w", err)
	}
	response, err := c.request(ctx, http.MethodPost, capture.Path, bytes.NewReader(body), http.Header{
		"Accept": {"text/x-component"}, "Content-Type": {"application/json"}, "Origin": {"https://www.linkedin.com"},
		"Referer": {before.URL + "details/experience/edit/forms/" + capture.PositionID + "/"},
	})
	if err != nil {
		return result, fmt.Errorf("experience save outcome uncertain; inspect profile before retrying: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	response.Body.Close()
	if readErr != nil {
		return result, fmt.Errorf("read experience receipt; inspect profile before retrying: %w", readErr)
	}
	if err := service.ValidateExperienceResponse(data); err != nil {
		return result, fmt.Errorf("experience save outcome uncertain; inspect profile before retrying: %w", err)
	}
	result.Applied = true
	after, _, err := c.fullProfile(ctx, ref)
	if err != nil {
		return result, fmt.Errorf("experience saved but readback failed; do not resend: %w", err)
	}
	actual, err := positionDescription(after, capture.PositionID)
	if err != nil || !sameProfileText(actual, text) {
		return result, fmt.Errorf("experience save not verified in current profile; inspect before retrying")
	}
	// Mask only the changed description; all other sourced profile blocks must agree.
	masked := after
	for si, section := range masked.Sections {
		masked.Sections[si].Blocks = append([]profileBlock(nil), section.Blocks...)
		for bi, block := range section.Blocks {
			if sameProfileText(block.Text, text) {
				masked.Sections[si].Blocks[bi].Text = current
			}
		}
	}
	if !reflect.DeepEqual(before.Sections, masked.Sections) || before.Name != after.Name || before.Headline != after.Headline {
		return result, fmt.Errorf("description saved but other profile content changed; inspect backup %q before further edits", result.BackupPath)
	}
	result.Verified = true
	return result, nil
}

const experienceHelp = `Update an existing experience description on your own LinkedIn profile.

Usage:
  linkedin experience update <profile> --position-id ID --description-file FILE [--title TEXT] [--apply]
  linkedin experience update <profile> --position-id ID --har FILE --description-file FILE [--apply]

Without --har, load the fresh native form and preserve its other fields/media.
An optional --title changes the position title with the description. Verify
the fresh form and all profile intro fields, including the unchanged location.

Preview is read-only. --apply requires the human's explicit instruction for this
profile/content; never use it for a live test. A successful native Save HAR is
required. Only the description and its comparison baseline are changed; all
other captured fields and media are preserved. Notify network must be off.
The signed-in owner, exact position and current description are checked before
writing. A stale capture is rejected. A private backup and independent full
profile readback verify the change. No automatic retries. After any uncertain
outcome read the profile before trying again. HAR cookies/headers are not replayed.
`
