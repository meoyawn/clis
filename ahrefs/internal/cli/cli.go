package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/meoyawn/clis/ahrefs/internal/domain"
	"github.com/meoyawn/clis/ahrefs/internal/service"
)

func Run(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, help)
		return err
	}
	command := args[0]
	switch command {
	case "projects", "crawls", "issues", "issue", "pages", "auth":
	default:
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sessionFile := fs.String("session-file", "", "saved Ahrefs cookie jar")
	timeout := fs.Duration("timeout", 60*time.Second, "whole-command timeout")
	fs.Bool("json", false, "JSON output")
	var profile, cookieFile string
	if command == "auth" {
		fs.StringVar(&profile, "browser-profile", "Default", "Chrome profile for auth import")
		fs.StringVar(&cookieFile, "cookies-file", "", "SweetCookie inline file for auth import")
	}
	var crawl, comparison, issueID, filterID, columns, level, search, sortValue string
	var all bool
	var limit, offset int
	if command == "issues" || command == "pages" {
		fs.StringVar(&crawl, "crawl", "", "completed crawl timestamp")
		fs.StringVar(&comparison, "compare-with", "", "earlier crawl or none")
		fs.BoolVar(&all, "all", false, "include all issues / fetch all pages")
	}
	if command == "issues" {
		fs.StringVar(&level, "level", "", "issue level")
		fs.StringVar(&search, "search", "", "issue name substring")
	}
	if command == "pages" {
		fs.StringVar(&issueID, "issue", "", "issue ID")
		fs.StringVar(&filterID, "filter-id", "", "saved filter ID")
		fs.StringVar(&columns, "columns", "", "page fields")
		fs.StringVar(&sortValue, "sort", "pageRating:desc", "field:asc|desc")
		fs.IntVar(&limit, "limit", 50, "rows per request")
		fs.IntVar(&offset, "offset", 0, "zero-based row offset")
	}
	if err := parseOptions(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, help)
		}
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("invalid timeout %s; expected a positive duration", *timeout)
	}
	want := 1
	if command == "projects" {
		want = 0
	} else if command == "issue" {
		want = 2
	}
	if fs.NArg() != want {
		return fmt.Errorf("%s expects %d positional arguments; use --help", command, want)
	}
	if command == "auth" && fs.Arg(0) != "status" && fs.Arg(0) != "import" {
		return fmt.Errorf("unknown auth command %q; use auth import or auth status", fs.Arg(0))
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if command == "auth" && fs.Arg(0) == "status" && (provided["browser-profile"] || provided["cookies-file"]) {
		return fmt.Errorf("--browser-profile and --cookies-file apply to auth import; auth status reads the saved jar")
	}
	var q domain.Query
	var err error
	if command != "projects" && command != "auth" {
		q, err = domain.ParseTarget(fs.Arg(0))
		if err != nil {
			return err
		}
	}
	if provided["crawl"] {
		q.Crawl, err = domain.NormalizeTimestamp(crawl)
		if err != nil {
			return err
		}
	}
	if provided["compare-with"] {
		if comparison == "none" {
			comparison = ""
		} else {
			comparison, err = domain.NormalizeTimestamp(comparison)
			if err != nil {
				return err
			}
			if comparison == "" {
				return fmt.Errorf("invalid compare-with value; use a timestamp or none")
			}
		}
		q.CompareWith = &comparison
	}
	for _, override := range []struct {
		flag  string
		value string
		out   *string
	}{{"issue", issueID, &q.IssueID}, {"filter-id", filterID, &q.FilterID}} {
		if provided[override.flag] {
			if override.value == "" {
				return fmt.Errorf("--%s must not be empty", override.flag)
			}
			*override.out = override.value
		}
	}
	if provided["columns"] {
		q.Columns, err = domain.ParseColumns(columns)
		if err != nil {
			return err
		}
	}
	if command == "issue" {
		if q.IssueID != "" && q.IssueID != fs.Arg(1) {
			return fmt.Errorf("issue ID %q differs from URL issueId %q", fs.Arg(1), q.IssueID)
		}
		q.IssueID = fs.Arg(1)
	}
	if command != "projects" && command != "auth" {
		if err := q.Validate(); err != nil {
			return err
		}
	}
	if command == "issues" && (q.IssueID != "" || q.FilterID != "" || len(q.Columns) > 0) {
		return fmt.Errorf("issues expects an All issues URL; use pages for Data Explorer URLs")
	}
	issueOptions := service.IssueOptions{All: all, Search: search}
	if provided["level"] {
		parsed, err := domain.NewLevelFromValue(level)
		if err != nil {
			return err
		}
		issueOptions.Level = &parsed
	}
	var pageOptions service.PageOptions
	if command == "pages" {
		field, direction, ok := strings.Cut(sortValue, ":")
		if !ok {
			direction = "desc"
		}
		parsed, err := domain.NewDirectionFromValue(direction)
		if err != nil {
			return err
		}
		pageOptions = service.PageOptions{Limit: limit, Offset: offset, All: all, SortBy: field, Direction: parsed}
		if err := pageOptions.Validate(); err != nil {
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
	if command == "auth" {
		var status service.AuthStatus
		if fs.Arg(0) == "import" {
			status, err = service.ImportSession(ctx, *sessionFile, profile, cookieFile)
		} else {
			status, err = service.SessionStatus(*sessionFile)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	}
	httpClient, err := service.SessionClient(*sessionFile)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	client := service.NewClient(httpClient)
	var result any
	switch command {
	case "projects":
		result, err = client.Projects(ctx)
	case "crawls":
		result, err = client.Crawls(ctx, q.ProjectID)
	case "issues":
		result, err = client.Issues(ctx, q, issueOptions)
	case "issue":
		result, err = client.Issue(ctx, q.ProjectID, q.IssueID)
	case "pages":
		result, err = client.Pages(ctx, q, pageOptions)
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func writeJSON(stdout io.Writer, result any) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
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
