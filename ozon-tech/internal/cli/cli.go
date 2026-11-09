package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/meoyawn/clis/ozon-tech/internal/domain"
	"github.com/meoyawn/clis/ozon-tech/internal/service"
)

const help = `ozon-tech: public Ozon Tech vacancy catalog using imported Chrome cookies.

  auth import [--browser-profile Default] [--cookies-file FILE]
  auth status [--check]
  vacancies [--details] [--concurrency 4]
            [--direction Разработка] [--work-format "Удалённая работа"]
  vacancy ID

All commands accept --session-file FILE and --timeout 3m after the command.
vacancies always fetches every page (20 items per request), validates the
reported total and unique IDs, and emits JSON only after the complete fetch.
--details includes every full description. Empty work_formats from the source
remain empty, including when the site returns them for a work-format filter.
auth status reads only the saved cookie jar; --check also tests live API access.
Only auth import reads Chrome/Keychain. Open ozon.tech in Chrome first, then
import again if the API returns an antibot challenge. Cookies stay in the OS
config directory, never in JSON output. This CLI does not submit applications.
`

func Run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, help)
		return err
	}
	command, rest := args[0], args[1:]
	if command == "auth" {
		if len(rest) == 0 || (rest[0] != "import" && rest[0] != "status") {
			return fmt.Errorf("expected auth import or auth status; use --help")
		}
		command, rest = "auth "+rest[0], rest[1:]
	}
	if command != "vacancies" && command != "vacancy" && command != "auth import" && command != "auth status" {
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("session-file", "", "saved cookie jar")
	timeout := fs.Duration("timeout", 3*time.Minute, "whole-command timeout")
	var profile, cookieFile, direction, format string
	var check, details bool
	concurrency := 4
	switch command {
	case "auth import":
		fs.StringVar(&profile, "browser-profile", "Default", "Chrome profile")
		fs.StringVar(&cookieFile, "cookies-file", "", "SweetCookie inline file")
	case "auth status":
		fs.BoolVar(&check, "check", false, "test live API access")
	case "vacancies":
		fs.StringVar(&direction, "direction", "Разработка", "direction filter")
		fs.StringVar(&format, "work-format", "Удалённая работа", "work-format filter")
		fs.BoolVar(&details, "details", false, "fetch every full description")
		fs.IntVar(&concurrency, "concurrency", 4, "parallel description requests, 1 to 16")
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, help)
		}
		return err
	}
	want := 0
	if command == "vacancy" {
		want = 1
	}
	if fs.NArg() != want {
		return fmt.Errorf("%s expects %d positional arguments; use --help (flags precede ID)", command, want)
	}
	if *timeout <= 0 {
		return fmt.Errorf("invalid timeout %s; expected a positive duration", *timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var err error
	if *path == "" {
		*path, err = service.SessionPath()
		if err != nil {
			return err
		}
	}
	if command == "auth import" {
		status, err := service.ImportSession(ctx, *path, profile, cookieFile)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	}
	if command == "auth status" {
		status, err := service.SessionStatus(*path)
		if err != nil {
			return err
		}
		if check {
			httpClient, err := service.SessionClient(*path)
			if err != nil {
				return err
			}
			defer httpClient.CloseIdleConnections()
			if err := service.NewClient(httpClient).CheckAccess(ctx); err != nil {
				return err
			}
			available := true
			status.APIAvailable = &available
		}
		return writeJSON(stdout, status)
	}
	httpClient, err := service.SessionClient(*path)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	client := service.NewClient(httpClient)
	var result any
	if command == "vacancy" {
		result, err = client.Vacancy(ctx, fs.Arg(0))
	} else {
		result, err = client.Catalog(ctx, domain.Filters{Directions: []string{direction}, WorkFormats: []string{format}}, details, concurrency)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func writeJSON(stdout io.Writer, value any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
