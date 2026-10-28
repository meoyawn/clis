package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

func runProfile(ctx context.Context, args []string, store authStore, out, errOut io.Writer) error {
	if len(args) == 0 || isHelpFlag(args[0]) {
		_, err := io.WriteString(out, profileEditHelp)
		return err
	}
	if args[0] == "headline" {
		return runHeadline(ctx, args, store, out, errOut)
	}
	if args[0] == "project" {
		return runProfileProject(ctx, args[1:], store, out, errOut)
	}
	if args[0] != "about" {
		return fmt.Errorf("unknown profile command %q; expected headline, about or project", args[0])
	}
	fs := flag.NewFlagSet("profile about", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("text-file", "", "UTF-8 About text")
	apply := fs.Bool("apply", false, "save the requested About update")
	args, err := parseFlags(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		_, err := io.WriteString(out, profileEditHelp)
		return err
	}
	if err != nil {
		return err
	}
	if len(args) != 1 || *file == "" {
		return fmt.Errorf("profile about requires one profile and --text-file FILE")
	}
	ref, err := parseProfile(args[0])
	if err != nil {
		return err
	}
	text, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	if err := validateProfileText("About", string(text), 2600); err != nil {
		return err
	}
	c, err := newLinkedinClient(store)
	if err != nil {
		return err
	}
	spec := profileFormSpec{path: "edit/forms/summary/new/", screen: "com.linkedin.sdui.flagshipnav.profile.ProfileAboutForm", initialRequest: saveAbout, saveRequest: saveAbout, fieldRequest: saveAbout}
	result, err := c.editProfileForm(ctx, ref, spec, map[string]any{"about": string(text)}, *apply, profileBackupDir(store))
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

func validateProfileText(field, text string, limit int) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(utf16.Encode([]rune(text))) > limit {
		return fmt.Errorf("profile %s must contain 1–%d UTF-16 code units of valid text", field, limit)
	}
	return nil
}

const profileEditHelp = `Edit your own profile through fresh native LinkedIn forms.

Usage:
  linkedin profile headline <person> --text TEXT [--apply]
  linkedin profile about <person> --text-file FILE [--apply]
  linkedin profile project <person> --file FILE [--apply]

Preview does not write. --apply saves the user's requested content and verifies
the fresh form, account identity, headline and all other intro fields, including
location. Existing top skills are preserved. Private before/receipt/readback
records are saved. No automatic retries; inspect after any uncertain result.
`
