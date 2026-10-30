package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

const usage = `Usage:
  invitro search [--include-filters] [--city CITY] [--page 1] [--type analyses|complexes|checkups] <query>
  invitro show [--full] [--city CITY] [--type TYPE] <id-or-url>
  invitro promos [--city CITY] [--page 1] [--full] [promotion-slug-or-url]
  invitro config
  invitro config set city <slug>

Search filters: --available-at-home[=false], --is-promo[=false], --deadline DAYS
--include-filters adds current filter definitions and CLI flag names to search JSON.
Promos lists active website offers; a slug fetches terms and live promotional article prices.
All prices are in RUB. Promotional articles do not automatically discount regular
catalog products; check linked eligibility, collection fees and bonus rules.
An empty additional_services list does not establish free collection.
Search returns one page of 20 results. Show accepts a website ID, UUID, or URL.
City comes from --city, a product URL, or config; there is no default city.
--full includes all published instructions and the source JSON.
--timeout defaults to 90s; --user-agent overrides the HTTP User-Agent.
Anonymous public APIs need no browser cookies.
No automatic pagination, retries, browser automation, or account access.
`

// Main runs the command-line application.
func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invitro:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runWith(ctx, args, stdout, stderr, defaultPaths, siteOrigin)
}

func runWith(ctx context.Context, args []string, stdout, stderr io.Writer, locate func() (appPaths, error), origin string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command; %s", usage)
	}
	switch args[0] {
	case "help", "--help", "-h":
		_, err := io.WriteString(stdout, usage)
		return err
	case "config":
		paths, err := locate()
		if err != nil {
			return err
		}
		return runConfig(ctx, args[1:], stdout, paths.config)
	case "promos":
		return runPromos(ctx, args[1:], stdout, stderr, locate, origin)
	case "search", "show":
	default:
		return fmt.Errorf("unknown command %q; %s", args[0], usage)
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	city := flags.String("city", "", "city URL slug, e.g. kazan or moscow")
	kindValue := flags.String("type", "analyses", "analyses, complexes, or checkups")
	timeout := flags.Duration("timeout", 90*time.Second, "timeout for the entire command")
	userAgent := flags.String("user-agent", defaultUserAgent, "HTTP User-Agent")
	page, full, sourceURL := 1, false, ""
	includeFilters := false
	filters := searchFilters{}
	if command == "search" {
		flags.IntVar(&page, "page", 1, "one page of 20 results")
		flags.BoolVar(&includeFilters, "include-filters", false, "include current filter definitions and CLI flag names (one extra request)")
		flags.Var(&filters.AtHome, "available-at-home", "filter home collection availability")
		flags.Var(&filters.Promo, "is-promo", "filter promotional tests")
		flags.IntVar(&filters.Deadline, "deadline", -1, "maximum turnaround in days (0 means today)")
	} else {
		flags.BoolVar(&full, "full", false, "include all instructions and source JSON")
		flags.StringVar(&sourceURL, "url", "", "canonical URL for the supplied ID (Planeta compatibility)")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %q must be positive", timeout.String())
	}
	if page < 1 || page > maxPage {
		return fmt.Errorf("page %d must be between 1 and %d", page, maxPage)
	}
	if flags.Lookup("deadline") != nil && flagSet(flags, "deadline") && filters.Deadline < 0 {
		return fmt.Errorf("deadline %d must be nonnegative", filters.Deadline)
	}
	kind, err := NewProductKindFromValue(*kindValue)
	if err != nil {
		return err
	}
	if command == "search" && kind == Checkups() && (filters.AtHome.set || filters.Deadline >= 0) {
		return fmt.Errorf("type %q supports only the --is-promo filter", kind.value)
	}
	query := strings.TrimSpace(strings.Join(flags.Args(), " "))
	var ref productRef
	if command == "search" {
		if query == "" {
			return fmt.Errorf("query %q must not be empty", query)
		}
	} else {
		if flags.NArg() != 1 {
			return fmt.Errorf("show expects one ID or URL, got %d arguments", flags.NArg())
		}
		ref, err = parseReference(flags.Arg(0))
		if err != nil {
			return err
		}
		if sourceURL != "" {
			urlRef, parseErr := parseReference(sourceURL)
			if parseErr != nil {
				return parseErr
			}
			if urlRef.City == "" || ref.City != "" || urlRef.ID != ref.ID {
				return fmt.Errorf("url %q must be a product URL matching ID %q", sourceURL, ref.ID)
			}
			ref = urlRef
		}
		if ref.Kind.value != "" {
			if flagSet(flags, "type") && kind != ref.Kind {
				return fmt.Errorf("type %q conflicts with product URL type %q", kind.value, ref.Kind.value)
			}
			kind = ref.Kind
		}
		if !flagSet(flags, "city") && ref.City != "" {
			*city = ref.City
		}
	}
	paths, err := locate()
	if err != nil {
		return err
	}
	config, err := loadConfig(paths.config)
	if err != nil {
		return err
	}
	if !flagSet(flags, "city") && *city == "" {
		*city = config.City
	}
	if err := validateCity(*city); err != nil {
		return err
	}
	if command == "show" && ref.Kind.value == "" && !flagSet(flags, "type") {
		cached, err := paths.lookup(*city, ref.ID)
		if err != nil {
			return err
		}
		if cached != nil {
			kind, err = NewProductKindFromValue(cached.Type)
			if err != nil {
				return err
			}
		}
	}
	client, err := newClient(origin, *userAgent)
	if err != nil {
		return err
	}
	defer client.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if command == "search" {
		result, err := client.Search(ctx, query, *city, kind, page, filters, includeFilters)
		if err != nil {
			return err
		}
		if err := paths.remember(*city, result.Results); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	result, err := client.Show(ctx, ref, *city, kind, full)
	if err != nil {
		return err
	}
	if err := paths.remember(*city, []product{result.product}); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func flagSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// Like Planeta, options may precede or follow positional arguments.
func parseOptions(flags *flag.FlagSet, args []string) error {
	var options, positional []string
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
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if option := flags.Lookup(name); option != nil && !hasValue {
			boolFlag, isBool := option.Value.(interface{ IsBoolFlag() bool })
			if !isBool || !boolFlag.IsBoolFlag() {
				if i+1 == len(args) {
					return fmt.Errorf("flag %s requires a value", arg)
				}
				i++
				options = append(options, args[i])
			}
		}
	}
	return flags.Parse(append(append(options, "--"), positional...))
}

func writeJSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
