package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/meoyawn/clis/habr-career/internal/domain"
	"github.com/meoyawn/clis/habr-career/internal/service"
)

func Run(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) == 0 || args[0] == "help" {
		_, err := io.WriteString(stdout, help)
		return err
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			_, err := io.WriteString(stdout, help)
			return err
		}
	}
	if len(args) < 2 {
		return fmt.Errorf("invalid command %q; run --help", strings.Join(args, " "))
	}
	command := strings.Join(args[:2], " ")
	optionStart := 2
	if command == "profile specialization" {
		if len(args) < 3 {
			return fmt.Errorf("profile specialization requires get, update or verify")
		}
		command += " " + args[2]
		optionStart = 3
	}
	switch command {
	case "auth import", "auth status", "profile get", "profile specialization get", "profile specialization update", "profile specialization verify", "resume get", "resume view", "resume experiences", "resume experience", "resume update", "resume verify", "vacancy get", "vacancy response-options", "applications list", "application get", "application letter", "application prepare", "application submit", "application verify", "application update", "har inspect":
	default:
		return fmt.Errorf("unknown command %q; run --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sessionFile := fs.String("session-file", "", "saved cookie jar")
	output := fs.String("output", "", "private JSON output")
	timeout := fs.Duration("timeout", 90*time.Second, "whole-command deadline")
	var profile, cookies, resume, textFile, draftFile, approved, experience, descriptionFile, backupDir, patchFile string
	var send, apply bool
	if command == "auth import" {
		fs.StringVar(&profile, "browser-profile", "Default", "Chrome profile")
		fs.StringVar(&cookies, "cookies-file", "", "SweetCookie inline cookie file")
	}
	if strings.HasPrefix(command, "application ") && command != "application submit" && command != "application verify" {
		fs.StringVar(&resume, "resume", "", "explicit stable native resume alias")
	}
	if command == "application prepare" || command == "application update" {
		fs.StringVar(&textFile, "text-file", "", "complete UTF-8 cover letter")
	}
	if command == "resume experience" || command == "resume update" || command == "resume verify" {
		fs.StringVar(&experience, "experience", "", "explicit native numeric experience ID")
	}
	if command == "resume update" || command == "resume verify" {
		fs.StringVar(&descriptionFile, "description-file", "", "exact native HTML experience description")
	}
	if command == "resume update" || command == "application update" || command == "profile specialization update" {
		fs.BoolVar(&apply, "apply", false, "apply an authorized update to an existing record")
		fs.StringVar(&backupDir, "backup-dir", "", "private before-state backup directory")
	}
	if command == "profile specialization update" || command == "profile specialization verify" {
		fs.StringVar(&patchFile, "patch", "", "JSON headline/languages patch for the single shared profile")
	}
	if command == "application submit" || command == "application verify" {
		fs.StringVar(&draftFile, "draft", "", "exact review package")
	}
	if command == "application submit" {
		fs.BoolVar(&send, "send", false, "send an authorized application")
		fs.StringVar(&approved, "approved", "", "unchanged review SHA-256")
	}
	if err := parseOptions(fs, args[optionStart:]); err != nil {
		return err
	}
	want := 0
	if args[0] == "resume" || args[0] == "vacancy" || args[0] == "har" || args[0] == "application" && args[1] != "submit" && args[1] != "verify" {
		want = 1
	}
	if fs.NArg() != want {
		return fmt.Errorf("%s expects %d positional arguments, got %q; run --help", command, want, fs.Args())
	}
	if *timeout <= 0 {
		return fmt.Errorf("invalid timeout %s; expected a positive duration", *timeout)
	}
	if command == "har inspect" {
		data, err := service.InspectHAR(fs.Arg(0))
		if err != nil {
			return err
		}
		return writeJSON(stdout, *output, data)
	}
	var vacancyID domain.VacancyID
	var resumeID domain.ResumeID
	var experienceID domain.ExperienceID
	var err error
	if args[0] == "vacancy" || args[0] == "application" && args[1] != "submit" && args[1] != "verify" {
		vacancyID, err = domain.NewVacancyIDFromValue(fs.Arg(0))
		if err != nil {
			return err
		}
	}
	if args[0] == "resume" {
		resume = fs.Arg(0)
	}
	if args[0] == "resume" || args[0] == "application" && args[1] != "submit" && args[1] != "verify" {
		resumeID, err = domain.NewResumeIDFromValue(resume)
		if err != nil {
			return err
		}
	}
	var letter string
	var description string
	var draft service.ApplicationDraft
	var patch service.Object
	if command == "profile specialization update" || command == "profile specialization verify" {
		if patchFile == "" {
			return fmt.Errorf("%s requires --patch FILE", command)
		}
		data, err := os.ReadFile(patchFile)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &patch); err != nil {
			return fmt.Errorf("decode specialization patch: %w", err)
		}
		patch, err = service.NormalizeSpecializationPatch(patch)
		if err != nil {
			return err
		}
	}
	if command == "resume experience" || command == "resume update" || command == "resume verify" {
		experienceID, err = domain.NewExperienceIDFromValue(experience)
		if err != nil {
			return err
		}
	}
	if command == "resume update" || command == "resume verify" {
		if descriptionFile == "" {
			return fmt.Errorf("%s requires --description-file FILE", command)
		}
		data, err := os.ReadFile(descriptionFile)
		if err != nil {
			return err
		}
		description = string(data)
	}
	if command == "application prepare" || command == "application update" {
		if textFile == "" {
			return fmt.Errorf("%s requires --text-file FILE", command)
		}
		data, err := os.ReadFile(textFile)
		if err != nil {
			return err
		}
		letter = string(data)
	}
	if command == "application submit" || command == "application verify" {
		if draftFile == "" {
			return fmt.Errorf("%s requires --draft FILE", command)
		}
		data, err := os.ReadFile(draftFile)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &draft); err != nil {
			return fmt.Errorf("decode application draft %q: %w", draftFile, err)
		}
		if err := service.VerifyDraft(draft, draft.ReviewSHA256); err != nil {
			return err
		}
		if command == "application submit" && !send {
			if approved != "" {
				return fmt.Errorf("--approved requires --send and prior user authorization")
			}
			return writeJSON(stdout, *output, draft)
		}
		if command == "application submit" {
			if err := service.VerifyDraft(draft, approved); err != nil {
				return err
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if *sessionFile == "" {
		*sessionFile, err = service.SessionPath()
		if err != nil {
			return err
		}
	}
	if command == "auth status" {
		status, err := service.SessionStatus(*sessionFile)
		if err != nil {
			return err
		}
		return writeJSON(stdout, *output, status)
	}
	unlock, err := service.LockAccount()
	if err != nil {
		return err
	}
	defer unlock()
	if command == "auth import" {
		status, err := service.ImportSession(ctx, *sessionFile, profile, cookies)
		if err != nil {
			return err
		}
		return writeJSON(stdout, *output, status)
	}
	httpClient, err := service.SessionClient(*sessionFile)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	client := service.NewClient(httpClient)
	var result any
	switch command {
	case "profile get":
		result, err = client.Profile(ctx)
	case "profile specialization get":
		result, err = client.Specialization(ctx)
	case "profile specialization update":
		result, err = client.UpdateSpecialization(ctx, patch, apply, backupDir)
	case "profile specialization verify":
		result, err = client.VerifySpecialization(ctx, patch)
	case "applications list":
		result, err = client.Applications(ctx)
	case "resume get", "resume view":
		result, err = client.Resume(ctx, resumeID)
	case "resume experiences":
		result, err = client.Experiences(ctx, resumeID)
	case "resume experience":
		result, err = client.Experience(ctx, resumeID, experienceID)
	case "resume update":
		result, err = client.UpdateExperience(ctx, resumeID, experienceID, description, apply, backupDir)
	case "resume verify":
		result, err = client.VerifyExperience(ctx, resumeID, experienceID, description)
	case "vacancy get", "vacancy response-options":
		result, err = client.Vacancy(ctx, vacancyID)
	case "application get", "application letter":
		result, err = client.Application(ctx, vacancyID, resumeID)
	case "application prepare":
		result, err = client.PrepareApplication(ctx, vacancyID, resumeID, letter)
	case "application submit":
		result, err = client.SubmitApplication(ctx, draft, approved)
	case "application verify":
		result, err = client.VerifyApplication(ctx, draft)
	case "application update":
		result, err = client.UpdateApplication(ctx, vacancyID, resumeID, letter, apply, backupDir)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, *output, result)
}

func writeJSON(stdout io.Writer, path string, value any) error {
	if path != "" {
		return service.SavePrivateJSON(path, value)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func parseOptions(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := fs.Lookup(name)
		if option == nil || hasValue {
			continue
		}
		boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 == len(args) {
			return fmt.Errorf("flag %s requires a value", arg)
		}
		i++
		flags = append(flags, args[i])
	}
	flags = append(flags, "--")
	return fs.Parse(append(flags, positional...))
}
