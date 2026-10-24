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

	megastroy "github.com/meoyawn/clis/megastroy/internal/service"
)

const usage = `megastroy — public Megastroy catalog in Kazan, JSON output

Usage:
  megastroy search QUERY [--include-filters] [--category ID]
                        [--filter KEY=VALUE]... [--sort ORDER]
                        [--page N] [--limit N]
  megastroy details CODE|URL
  megastroy image IMAGE_URL [--output FILE]
  megastroy auth import [--browser chrome] [--browser-profile PROFILE]
  megastroy auth status

Examples:
  megastroy auth import --browser chrome
  megastroy search полотенцесушитель --include-filters
  megastroy search полотенцесушитель --category 1102 --include-filters
  megastroy search полотенцесушитель --category 1102 --filter 'field79[]=Terminus'
  megastroy details 416479

Filters and category children come from the current search page. Copy filter
keys/values from --include-filters; ranges expose from_key and to_key.
Sort orders: popular, price, price-desc, sale, latest, striked-price.
Page numbers start at 1. Each data command fetches one page; --limit only trims
output. details always includes all published characteristics and dimensions.
Search cards and details include rating (displayed stars out of 5) and
review_count; unpublished values are null. Ratings need no additional request.
id and show are aliases for details.

Search and details return product image URLs in images. Use image with one of
those URLs to download a picture for viewing, with one HTTP request and no auth
required. JSON includes the absolute local path, content_type, and bytes.
Without --output a temporary file is created; existing files are never replaced.
Images are not fetched automatically by search or details.

All commands support --timeout 60s and --user-agent TEXT. JSON is the default;
--json is accepted for compatibility. Options can precede/follow positional
arguments; -- ends option parsing.

No browser is launched. auth import reads only anonymous DDoS-Guard cookies
from your existing browser; import/status make no HTTP requests. If access is
rejected, open kazan.megastroy.com in the browser and repeat auth import.
`

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ", ") }
func (r *repeated) Set(s string) error { *r = append(*r, s); return nil }

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	command := args[0]
	if command == "auth" {
		return runAuth(ctx, args[1:], stdout)
	}
	if command != "search" && command != "details" && command != "id" && command != "show" && command != "image" {
		return fmt.Errorf("unknown command %q; use search, details, image, or auth", command)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	timeout := flags.Duration("timeout", 60*time.Second, "whole-command timeout")
	userAgent := flags.String("user-agent", "", "HTTP User-Agent override")
	flags.Bool("json", false, "JSON output (default)")
	o := megastroy.SearchOptions{Page: 1}
	var rawFilters repeated
	var sortValue, category, output string
	if command == "image" {
		flags.StringVar(&output, "output", "", "save image to a new file; default is a temporary file")
	}
	if command == "search" {
		flags.BoolVar(&o.IncludeFilters, "include-filters", false, "include current filters and category hierarchy")
		flags.IntVar(&o.Page, "page", 1, "server page, starting at 1")
		flags.IntVar(&o.Limit, "limit", 0, "trim this page; 0 prints all results")
		flags.Var(&rawFilters, "filter", "KEY=VALUE; repeat for multiple values")
		flags.StringVar(&sortValue, "sort", "popular", "website sort order")
		flags.StringVar(&category, "category", "", "search category ID")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, usage)
		}
		return err
	}
	if err := validateCommon(*timeout, *userAgent); err != nil {
		return err
	}
	if command == "image" {
		if flags.NArg() != 1 {
			return fmt.Errorf("image requires exactly one URL from a product's images array")
		}
		client, err := megastroy.NewClient(megastroy.Session{UserAgent: *userAgent})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		result, err := client.Image(ctx, flags.Arg(0), output)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if command == "search" {
		o.Query = strings.TrimSpace(strings.Join(flags.Args(), " "))
		if o.Query == "" {
			return fmt.Errorf("search requires a nonempty query")
		}
		if o.Page < 1 {
			return fmt.Errorf("page %d must be at least 1", o.Page)
		}
		if o.Limit < 0 {
			return fmt.Errorf("limit %d must not be negative", o.Limit)
		}
		if category != "" {
			rawFilters = append(rawFilters, "category_id="+category)
		}
		var err error
		o.Filters, err = megastroy.ParseFilters(rawFilters)
		if err != nil {
			return err
		}
		o.Sort, err = megastroy.NewSortFromValue(sortValue)
		if err != nil {
			return err
		}
	} else if flags.NArg() != 1 {
		return fmt.Errorf("%s requires exactly one product code or URL", command)
	}
	store, err := megastroy.DefaultAuthStore()
	if err != nil {
		return err
	}
	session, err := store.Load()
	if err != nil {
		return err
	}
	if len(session.HTTPCookies(time.Now())) == 0 {
		return fmt.Errorf("no usable saved DDoS-Guard cookies; run megastroy auth import --browser chrome")
	}
	if *userAgent != "" {
		session.UserAgent = *userAgent
	}
	client, err := megastroy.NewClient(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var result any
	if command == "search" {
		result, err = client.Search(ctx, o)
	} else {
		result, err = client.Details(ctx, flags.Arg(0))
	}
	if err != nil {
		return err
	}
	if err = store.Save(ctx, client.Session()); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func runAuth(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("auth requires import or status")
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if args[0] != "import" && args[0] != "status" {
		return fmt.Errorf("unknown auth command %q; use import or status", args[0])
	}
	flags := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	timeout := flags.Duration("timeout", 60*time.Second, "whole-command timeout")
	userAgent := flags.String("user-agent", "", "HTTP User-Agent override")
	flags.Bool("json", false, "JSON output (default)")
	var browserValue, profile string
	if args[0] == "import" {
		flags.StringVar(&browserValue, "browser", "chrome", "browser containing a cleared Megastroy session")
		flags.StringVar(&profile, "browser-profile", "", "browser profile name, directory or cookie database path")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, usage)
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("auth %s does not accept positional arguments", args[0])
	}
	if err := validateCommon(*timeout, *userAgent); err != nil {
		return err
	}
	var browser megastroy.Browser
	if args[0] == "import" {
		var err error
		browser, err = megastroy.NewBrowserFromValue(browserValue)
		if err != nil {
			return err
		}
	}
	store, err := megastroy.DefaultAuthStore()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	var status *megastroy.AuthStatus
	if args[0] == "import" {
		status, err = store.Import(ctx, browser, profile, *userAgent)
	} else {
		status, err = store.Status()
	}
	if err != nil {
		return err
	}
	return writeJSON(stdout, status)
}

func validateCommon(timeout time.Duration, userAgent string) error {
	if timeout <= 0 {
		return fmt.Errorf("timeout %s must be positive", timeout)
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return fmt.Errorf("user-agent must not contain newlines")
	}
	return nil
}

func parseOptions(flags *flag.FlagSet, args []string) error {
	var options, positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals = append(positionals, arg)
			continue
		}
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if option := flags.Lookup(name); option != nil && !hasValue {
			boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
			if !ok || !boolean.IsBoolFlag() {
				if i+1 == len(args) {
					return fmt.Errorf("flag %s requires a value", arg)
				}
				i++
				options = append(options, args[i])
			}
		}
	}
	options = append(options, "--")
	options = append(options, positionals...)
	return flags.Parse(options)
}

func writeJSON(w io.Writer, v any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
