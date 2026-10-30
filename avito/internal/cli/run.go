package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode"

	avito "github.com/meoyawn/clis/avito/internal/service"
	"github.com/steipete/sweetcookie"
)

const usage = `Usage:
  avito auth import --browser chrome [--browser-profile PROFILE] [--json]
  avito auth status [--json]
  avito config
  avito config set <key> <value>
  avito filters [query] [--category-id ID] [--filter KEY=VALUE] [--only KEY]
  avito filters [query] --category-id ID --field KEY [--value-query TEXT] [--offset N] [--filter KEY=VALUE]
  avito search [query] [--category-id ID] [--filter KEY=VALUE] [--sort VALUE] [--page N] [--limit N]
  avito show <id-or-url>

Auth import supports chrome, firefox, brave, edge, and safari. Auth commands
check your sign-in with Avito and show the current account. Use --json for
diagnostics or scripts. Failed checks exit nonzero; failed imports keep the
previous saved session. Search/filters/show use the saved cookies. Request flags:
  --timeout 60s
  --user-agent TEXT

Search and filters:
  --city NAME             (resolve a Russian city name through Avito)
  --location-id ID        (override the saved city)
  --local-priority       (default: true; selected city first)
  --category-id ID        (default: no explicit category)
  --filter KEY=VALUE      (repeatable; keys/values come from filters)
  --sort VALUE           (value from filters.sorting)
Supply a query, or select --category-id to browse without keywords.

Search only:
  --page N               (default: 1; exactly one page)
  --limit N              (default: 10; 0 prints the full requested page)

Filters discovers categories, option values, selections, numeric range input
keys, and description keyword fields. No category or brand IDs are built in.
Copy its search_args array (flags only) into subsequent search/filters calls;
it retains Avito's inferred category parameters. Add --filter KEY=VALUE to refine.
For multiple:true, repeat a key for each value. Quote keys containing brackets.
For numericRange, use inputs[].key for each bound, e.g. --filter pmax=5000.
For keywords, pass text as the value. Search snippets may be truncated: use show
to confirm age, condition, and other details that do not have search filters.

Rerun filters with the current selection to discover dependent fields (such as
model after brand). --only KEY prints one field from that response. has_suggest
means more values may be available: --field KEY --value-query TEXT searches that
field's options directly in one request; copy search_args to preserve its scope.
Omit --value-query to list options, and use next_offset only while has_more is
true. --field and --only are different modes and cannot be combined.

Search returns applied_filters, search_args, prices, descriptions and pagination.
Search prices are in RUB; show retains the currency reported by the page.
An ignored filter/category/sort produces an error. Similar recommendations and
prices outside explicit bounds are excluded with excluded_count and a warning.
City-first ordering can include other cities; inspect each listing's city when
building a local price sample. These are asking prices, not completed sales.

Config shows all effective defaults as JSON. Config set saves a default in
avito/config.json in your user config directory, beside auth.json. Keys:
city, local-priority, timeout. Config shows only the city name; its numeric ID is
cached internally. No city is selected by default. Explicit flags override saved
preferences.
Use --local-priority=false to disable city-first ordering for one search.

Search/filters/show emit one JSON object. Search or filters makes one request, plus one city
lookup for an uncached city. Config set city resolves and caches a city once;
other config commands are local. --location-id skips resolution. Auth checks
and show make one request. Lookups use saved cookies. Input, missing-session,
or unknown-ID failures make zero requests. No retries, sleeps, redirects,
fallbacks, response cache, or automatic pagination. HTTP 429 exits nonzero and
includes Retry-After when supplied. The caller controls pacing and concurrency.

Show by ID uses the canonical URL remembered by web search or a previous show.
For an unseen ID, pass its full Avito listing URL.
`

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	config, err := avito.DefaultConfigStore()
	if err != nil {
		return err
	}
	return run(ctx, args, stdout, stderr, func() (dependencies, error) {
		index, err := avito.DefaultURLIndex()
		if err != nil {
			return dependencies{}, err
		}
		auth, err := avito.DefaultAuthStore()
		if err != nil {
			return dependencies{}, err
		}
		locations, err := avito.DefaultLocationCache()
		if err != nil {
			return dependencies{}, err
		}
		httpClient := avito.NewHTTPClient()
		client, err := avito.NewClient(httpClient, auth)
		return dependencies{client: client, index: index, locations: locations, readCookies: sweetcookie.Get, close: httpClient.CloseIdleConnections}, err
	}, config)
}

type dependencies struct {
	client      *avito.Client
	index       avito.URLIndex
	locations   avito.LocationCache
	readCookies avito.CookieReader
	close       func()
}

type setupFunc func() (dependencies, error)

func run(ctx context.Context, args []string, stdout, stderr io.Writer, setup setupFunc, config avito.ConfigStore) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command; %s", usage)
	}
	switch args[0] {
	case "--help", "-h", "help":
		_, err := io.WriteString(stdout, usage)
		return err
	case "auth":
		return runAuth(ctx, args[1:], stdout, stderr, setup, config)
	case "config":
		return runConfig(ctx, args[1:], stdout, setup, config)
	case "search", "filters", "show":
	default:
		return fmt.Errorf("unknown command %q; %s", args[0], usage)
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) } //nolint:errcheck
	timeout := flags.Duration("timeout", 60*time.Second, "request timeout")
	userAgent := flags.String("user-agent", "", "HTTP User-Agent override")
	page, locationID, categoryID := 1, int64(0), int64(0)
	var limit int
	var city string
	var localPriority bool
	filters := filterValues{}
	var sortValue, field, valueQuery, only string
	var offset int
	if args[0] == "search" || args[0] == "filters" {
		flags.Int64Var(&locationID, "location-id", 0, "Avito location ID; overrides the saved city")
		flags.StringVar(&city, "city", "", "Russian city name; overrides the saved location")
		flags.BoolVar(&localPriority, "local-priority", true, "ask Avito to show the selected city first")
		flags.Int64Var(&categoryID, "category-id", 0, "optional Avito category ID")
		flags.Var(filters, "filter", "KEY=VALUE from filters; repeat a key for multiple selections")
		flags.StringVar(&sortValue, "sort", "", "sorting value from filters.sorting")
	}
	if args[0] == "search" {
		flags.IntVar(&page, "page", 1, "one page number")
		flags.IntVar(&limit, "limit", 10, "maximum listings to print from the requested page; 0 prints all")
	}
	if args[0] == "filters" {
		flags.StringVar(&field, "field", "", "parameter key for direct option lookup, such as params[123]")
		flags.StringVar(&valueQuery, "value-query", "", "option text to look up with --field")
		flags.IntVar(&offset, "offset", 0, "one option page offset, with --field")
		flags.StringVar(&only, "only", "", "print only this filter key from the discovered form")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	var citySet, locationSet, sortSet, fieldSet, optionSet, onlySet bool
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "city":
			citySet = true
		case "location-id":
			locationSet = true
		case "sort":
			sortSet = true
		case "field":
			fieldSet = true
		case "value-query", "offset":
			optionSet = true
		case "only":
			onlySet = true
		}
	})
	if citySet && locationSet {
		return fmt.Errorf("--city and --location-id cannot be used together; choose one location")
	}
	if citySet {
		if err := avito.ValidateCity(city); err != nil {
			return err
		}
	}
	if err := applySavedConfig(flags, config); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %q must be positive", timeout.String())
	}
	if strings.ContainsAny(*userAgent, "\r\n") {
		return fmt.Errorf("user-agent must not contain newlines")
	}
	options := avito.SearchOptions{Query: strings.TrimSpace(strings.Join(flags.Args(), " ")), LocationID: locationID, LocalPriority: localPriority, CategoryID: categoryID, Page: page, Filters: url.Values(filters)}
	if sortSet {
		var err error
		options.Sort, err = avito.NewSortFromValue(sortValue)
		if err != nil {
			return err
		}
	}
	if optionSet && !fieldSet {
		return fmt.Errorf("--value-query and --offset require --field")
	}
	if fieldSet && onlySet {
		return fmt.Errorf("--field and --only cannot be combined")
	}
	if onlySet && strings.TrimSpace(only) == "" {
		return fmt.Errorf("--only needs a filter key")
	}
	if fieldSet {
		if err := avito.ValidateFilterValues(avito.FilterValuesOptions{Search: options, Key: field, ValueQuery: valueQuery, Offset: offset}); err != nil {
			return err
		}
	}
	var item avito.ItemReference
	if args[0] == "search" || args[0] == "filters" {
		if limit < 0 {
			return fmt.Errorf("limit %d must not be negative", limit)
		}
		if err := avito.ValidateSearchOptions(options); err != nil {
			return err
		}
		if locationSet {
			if _, err := avito.SearchURL(options); err != nil {
				return err
			}
		} else if city == "" {
			return fmt.Errorf("no city configured; run avito config set city <name>, or search with --city <name> or --location-id ID")
		}
	} else {
		if flags.NArg() != 1 {
			return fmt.Errorf("show expects one ID or URL, got %d arguments", flags.NArg())
		}
		var err error
		item, err = avito.ParseItemReference(flags.Arg(0))
		if err != nil {
			return err
		}
	}
	deps, err := setup()
	if err != nil {
		return err
	}
	if deps.close != nil {
		defer deps.close()
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	auth := avito.RequestOptions{UserAgent: *userAgent}
	if args[0] == "search" || args[0] == "filters" {
		var lookupRequests int
		if !locationSet {
			location, calls, err := deps.locations.Resolve(ctx, deps.client, city, auth)
			if err != nil {
				return err
			}
			options.LocationID = location.ID
			lookupRequests = calls
		}
		if fieldSet {
			result, err := deps.client.FilterValues(ctx, avito.FilterValuesOptions{Search: options, Key: field, ValueQuery: valueQuery, Offset: offset}, auth)
			if err != nil {
				return err
			}
			result.HTTPRequests += lookupRequests
			return writeJSON(stdout, result)
		}
		if args[0] == "filters" {
			result, err := deps.client.Filters(ctx, options, auth)
			if err != nil {
				return err
			}
			result.HTTPRequests += lookupRequests
			if onlySet {
				var selected []avito.Filter
				for _, filter := range result.Filters {
					if filter.Key == only {
						selected = append(selected, filter)
					}
				}
				if len(selected) == 0 {
					return fmt.Errorf("filter %q is absent from the current form; run filters without --only to discover keys", only)
				}
				result.Filters = selected
			}
			return writeJSON(stdout, result)
		}
		result, err := deps.client.Search(ctx, options, auth)
		if err != nil {
			return err
		}
		result.HTTPRequests += lookupRequests
		if err := deps.index.Remember(result.Results); err != nil {
			result.Warnings = append(result.Warnings, err.Error())
		}
		if limit > 0 && len(result.Results) > limit {
			result.Results = result.Results[:limit]
			result.Count = len(result.Results)
		}
		return writeJSON(stdout, result)
	}
	if item.URL == "" {
		item, err = deps.index.Lookup(item.ID)
		if err != nil {
			return err
		}
	}
	result, err := deps.client.Show(ctx, item, auth)
	if err != nil {
		return err
	}
	if err := deps.index.Remember([]avito.Listing{result.Listing}); err != nil {
		result.Warnings = append(result.Warnings, err.Error())
	}
	return writeJSON(stdout, result)
}

type filterValues url.Values

func (values filterValues) String() string { return url.Values(values).Encode() }

func (values filterValues) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || key == "" || value == "" {
		return fmt.Errorf("filter %q must have form KEY=VALUE", raw)
	}
	url.Values(values).Add(key, value)
	return nil
}

func runAuth(ctx context.Context, args []string, stdout, stderr io.Writer, setup setupFunc, config avito.ConfigStore) error {
	if len(args) == 0 {
		return fmt.Errorf("auth expects import or status; %s", usage)
	}
	command := args[0]
	if command == "--help" || command == "-h" || command == "help" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if command != "import" && command != "status" {
		return fmt.Errorf("unknown auth command %q; use import or status", command)
	}
	flags := flag.NewFlagSet("auth "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) } //nolint:errcheck
	var browserValue, profile string
	timeout := flags.Duration("timeout", 60*time.Second, "sign-in check and import timeout")
	userAgent := flags.String("user-agent", "", "HTTP User-Agent override")
	jsonOutput := flags.Bool("json", false, "print authentication details as JSON")
	if command == "import" {
		flags.StringVar(&browserValue, "browser", "", "browser to import cookies from (required)")
		flags.StringVar(&profile, "browser-profile", "", "browser profile name or directory")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("auth %s does not accept positional arguments", command)
	}
	if err := applySavedConfig(flags, config); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %q must be positive", timeout.String())
	}
	if strings.ContainsAny(*userAgent, "\r\n") {
		return fmt.Errorf("user-agent must not contain newlines")
	}
	var browser avito.Browser
	if command == "import" {
		if browserValue == "" {
			return fmt.Errorf("auth import requires --browser, for example --browser chrome")
		}
		var err error
		browser, err = avito.NewBrowserFromValue(browserValue)
		if err != nil {
			return err
		}
	}
	deps, err := setup()
	if err != nil {
		return err
	}
	if deps.close != nil {
		defer deps.close()
	}
	var status *avito.AuthStatus
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	options := avito.RequestOptions{UserAgent: *userAgent}
	if command == "status" {
		status, err = deps.client.AuthStatus(ctx, options)
	} else {
		status, err = deps.client.ImportAuth(ctx, browser, profile, deps.readCookies, options)
	}
	if *jsonOutput && status != nil {
		if outputErr := writeJSON(stdout, status); outputErr != nil {
			return outputErr
		}
		return err
	}
	if err != nil {
		return err
	}
	message := "Signed in to Avito."
	if status.Account != nil && terminalText(status.Account.Name) != "" {
		message = "Signed in as " + terminalText(status.Account.Name) + "."
	}
	if command == "import" {
		message += " Session saved."
	}
	_, err = fmt.Fprintln(stdout, message)
	return err
}

func terminalText(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}), " ")
}

// Flags may follow positionals; -- allows searches beginning with '-'.
func parseOptions(flags *flag.FlagSet, args []string) error {
	var options, positional []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			positional = append(positional, args...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := flags.Lookup(name)
		if option != nil && !hasValue {
			boolFlag, isBool := option.Value.(interface{ IsBoolFlag() bool })
			if !isBool || !boolFlag.IsBoolFlag() {
				if len(args) == 0 {
					return fmt.Errorf("flag %s requires a value", arg)
				}
				options = append(options, args[0])
				args = args[1:]
			}
		}
	}
	return flags.Parse(append(append(options, "--"), positional...))
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write JSON: %w", err)
	}
	return nil
}
