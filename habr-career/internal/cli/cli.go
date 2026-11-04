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
	switch command {
	case "auth import", "auth status", "resumes list", "profile get", "resume get", "resume view", "vacancy get", "vacancy response-options", "application get", "application letter", "application prepare", "application submit", "har inspect":
	default:
		return fmt.Errorf("unknown command %q; run --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sessionFile := fs.String("session-file", "", "saved cookie jar")
	output := fs.String("output", "", "private JSON output")
	timeout := fs.Duration("timeout", 90*time.Second, "whole-command deadline")
	var profile, cookies, resume, textFile, draftFile, approved string
	var send bool
	if command == "auth import" {
		fs.StringVar(&profile, "browser-profile", "Default", "Chrome profile")
		fs.StringVar(&cookies, "cookies-file", "", "SweetCookie inline cookie file")
	}
	if strings.HasPrefix(command, "application ") && command != "application submit" {
		fs.StringVar(&resume, "resume", "", "explicit stable native resume alias")
	}
	if command == "application prepare" {
		fs.StringVar(&textFile, "text-file", "", "complete UTF-8 cover letter")
	}
	if command == "application submit" {
		fs.StringVar(&draftFile, "draft", "", "exact review package")
		fs.BoolVar(&send, "send", false, "send an authorized application")
		fs.StringVar(&approved, "approved", "", "unchanged review SHA-256")
	}
	if err := parseOptions(fs, args[2:]); err != nil {
		return err
	}
	want := 0
	if args[0] == "resume" || args[0] == "vacancy" || args[0] == "har" || args[0] == "application" && args[1] != "submit" {
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
	var err error
	if args[0] == "vacancy" || args[0] == "application" && args[1] != "submit" {
		vacancyID, err = domain.NewVacancyIDFromValue(fs.Arg(0))
		if err != nil {
			return err
		}
	}
	if args[0] == "resume" {
		resume = fs.Arg(0)
	}
	if args[0] == "resume" || args[0] == "application" && args[1] != "submit" {
		resumeID, err = domain.NewResumeIDFromValue(resume)
		if err != nil {
			return err
		}
	}
	var letter string
	var draft service.ApplicationDraft
	if command == "application prepare" {
		if textFile == "" {
			return fmt.Errorf("application prepare requires --text-file FILE")
		}
		data, err := os.ReadFile(textFile)
		if err != nil {
			return err
		}
		letter = string(data)
	}
	if command == "application submit" {
		if draftFile == "" {
			return fmt.Errorf("application submit requires --draft FILE")
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
		if !send {
			if approved != "" {
				return fmt.Errorf("--approved requires --send and prior user authorization")
			}
			return writeJSON(stdout, *output, draft)
		}
		if err := service.VerifyDraft(draft, approved); err != nil {
			return err
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
	case "resumes list":
		result, err = client.Resumes(ctx)
	case "profile get":
		result, err = client.Profile(ctx)
	case "resume get", "resume view":
		result, err = client.Resume(ctx, resumeID)
	case "vacancy get", "vacancy response-options":
		result, err = client.Vacancy(ctx, vacancyID)
	case "application get", "application letter":
		result, err = client.Application(ctx, vacancyID, resumeID)
	case "application prepare":
		result, err = client.PrepareApplication(ctx, vacancyID, resumeID, letter)
	case "application submit":
		result, err = client.SubmitApplication(ctx, draft, approved)
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
