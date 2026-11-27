package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/meoyawn/clis/lemana/internal/domain"
	"github.com/meoyawn/clis/lemana/internal/service"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const usage = `lemana — public catalog in Kazan; JSON output

Usage:
  lemana search QUERY [--include-filters] [--filter KEY=VALUE]...
  lemana list CATEGORY_PATH_OR_URL [--include-filters] [--filter KEY=VALUE]...
  lemana details PRODUCT_URL [--availability]
  lemana auth import [--browser chrome] [--browser-profile Default]
  lemana auth status

Options:
  --page N              One server page, starting at 1 (default 1)
  --limit N             Trim this page locally; 0 prints all its products
  --sort ORDER          popular (default), price, price-desc, rating, sale
  --filter KEY=VALUE    Repeat exact keys and values from --include-filters
  --timeout 60s         Whole-command timeout
  --user-agent TEXT     Override the saved browser User-Agent
  --session-file PATH   Override the owner-only local session file
  --json                Accepted; JSON is always the output format

Search/list normally make one HTTP request, including filters when requested.
For ordinary checkbox facets use id=values.name; values.value is a count.
Repeated values are joined with the website's underscore separator.
Details include published characteristics, direct image URLs and rating.
rating/review_count describe this exact item, never a variants total.
product_rating/product_review_count retain the same exact-item values.
group_rating/group_review_count hold variant-group aggregates separately.
Unknowns are null; an explicit zero own count stays zero without fallback.
Sorts follow the site's ordering; apply exact-item thresholds locally.
Details include the first review page already delivered in the HTML.
Details list the variant group in the same response: variants hold every
existing product (id, url, price, values in variant_axes order). Combinations
missing from variants are not sold; the website greys out their selector values.
--availability adds one POST for regional stock; otherwise it is not requested.

No images are fetched automatically. Download selected URLs with curl/wget and
view saved files locally. There is no image subcommand.

Requests carry the site's protection cookie, which lives about 16 minutes.
When it is missing or stale, or a request hits a protection page or HTTP
401/403/429, the command loads the storefront in a hidden system web view
with a fresh in-memory session, lets the site's own check run there (at most
30s), saves the renewed cookie and retries once. No browser is opened and the
web view writes nothing to disk. Only protection/region cookies are saved,
with owner-only permissions. auth import optionally seeds them from Chrome
with sweetcookie; import/status are local and make no HTTP requests.
`

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ", ") }
func (r *repeated) Set(s string) error { *r = append(*r, s); return nil }

// renewFunc passes the site's check in a real browser engine and returns the
// cookies that engine then holds.
type renewFunc func(ctx context.Context) ([]*http.Cookie, error)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return run(ctx, args, stdout, stderr, webviewProtection)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, renewCookies renewFunc) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	command := args[0]
	if command != "search" && command != "list" && command != "details" && command != "auth" {
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeout := fs.Duration("timeout", 60*time.Second, "whole-command timeout")
	ua := fs.String("user-agent", "", "HTTP User-Agent")
	sessionPath := fs.String("session-file", "", "local session file")
	fs.Bool("json", false, "JSON output")
	var o service.Options
	var filters repeated
	var sortValue string
	var availability bool
	var section string
	if command == "search" || command == "list" {
		fs.IntVar(&o.Page, "page", 1, "server page")
		fs.IntVar(&o.Limit, "limit", 0, "local output limit")
		fs.BoolVar(&o.IncludeFilters, "include-filters", false, "include current filters")
		fs.StringVar(&sortValue, "sort", "popular", "sort order")
		fs.Var(&filters, "filter", "KEY=VALUE")

	}
	if command == "details" {
		fs.BoolVar(&availability, "availability", false, "also fetch regional stock (one extra request)")
	}
	var profile, browser string
	if command == "auth" {
		fs.StringVar(&profile, "browser-profile", "Default", "Chrome profile name or path")
		fs.StringVar(&browser, "browser", "chrome", "Chrome only")
	}
	if err := parseOptions(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(stdout, usage)
		}
		return err
	}
	if *timeout <= 0 || strings.ContainsAny(*ua, "\r\n") {
		return fmt.Errorf("timeout must be positive and user-agent must not contain newlines")
	}
	if command == "search" || command == "list" {
		var err error
		o.Sort, err = domain.NewSortFromValue(sortValue)
		if err != nil {
			return err
		}
		if err = o.Validate(); err != nil {
			return err
		}
		o.Filters = url.Values{}
		for _, raw := range filters {
			k, v, ok := strings.Cut(raw, "=")
			if !ok || k == "" || v == "" {
				return fmt.Errorf("filter %q must be KEY=VALUE", raw)
			}
			o.Filters.Add(k, v)
		}
	}
	if command == "search" && fs.NArg() == 0 {
		return fmt.Errorf("search requires a query")
	}
	if command == "list" && ((section == "" && fs.NArg() != 1) || (section != "" && fs.NArg() != 0)) {
		return fmt.Errorf("list requires one category path/URL or --section ID")
	}
	if command == "details" && fs.NArg() != 1 {
		return fmt.Errorf("details requires exactly one product URL")
	}
	if command == "auth" && (fs.NArg() != 1 || (fs.Arg(0) != "import" && fs.Arg(0) != "status")) {
		return fmt.Errorf("auth requires import or status")
	}
	if *sessionPath == "" {
		p, err := service.SessionPath()
		if err != nil {
			return err
		}
		*sessionPath = p
	}
	if command == "auth" {
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if browser != "chrome" {
			return fmt.Errorf("browser %q is unsupported; use chrome", browser)
		}
		if fs.Arg(0) == "import" {
			status, err := service.ImportSession(ctx, *sessionPath, profile, *ua)
			if err != nil {
				return err
			}
			return writeJSON(stdout, status)
		}
		s, err := service.LoadSession(*sessionPath)
		if err != nil {
			return err
		}
		return writeJSON(stdout, service.SessionStatus(*sessionPath, s))
	}
	s, err := service.LoadSession(*sessionPath)
	if err != nil {
		return err
	}
	renewed := false
	renew := func() error {
		if renewed {
			return errors.New("the renewed protection cookie was rejected too")
		}
		renewed = true
		fresh, err := renewCookies(ctx)
		if err != nil {
			return fmt.Errorf("renew the catalog protection cookie: %w", err)
		}
		if s, err = service.RenewProtection(s, fresh, protectionMargin); err != nil {
			return err
		}
		return service.SaveSession(*sessionPath, s)
	}
	if service.FreshProtection(s, protectionMargin) == "" {
		if err := renew(); err != nil {
			return err
		}
	}
	fetch := func(s service.Session) (*domain.Result, *service.Client, error) {
		if *ua != "" {
			s.UserAgent = *ua
		}
		client, err := service.NewClient(s)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		var result *domain.Result
		switch command {
		case "search":
			result, err = client.Search(ctx, strings.Join(fs.Args(), " "), o)
		case "list":
			result, err = client.List(ctx, fs.Arg(0), o)
		case "details":
			result, err = client.Details(ctx, fs.Arg(0), availability)
		}
		return result, client, err
	}
	result, client, err := fetch(s)
	var blocked *service.BlockedError
	if errors.As(err, &blocked) {
		if renewErr := renew(); renewErr != nil {
			return fmt.Errorf("%w; %w", err, renewErr)
		}
		result, client, err = fetch(s)
	}
	if err != nil {
		return err
	}
	if err = service.SaveSession(*sessionPath, client.Session()); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

// protectionMargin treats a cookie expiring sooner as stale.
const protectionMargin = 30 * time.Second

func parseOptions(fs *flag.FlagSet, args []string) error {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		name, _, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if f := fs.Lookup(name); f != nil && !hasValue {
			b, ok := f.Value.(interface{ IsBoolFlag() bool })
			if !ok || !b.IsBoolFlag() {
				if i+1 == len(args) {
					return fmt.Errorf("flag %s requires a value", a)
				}
				i++
				flags = append(flags, args[i])
			}
		}
	}
	flags = append(flags, "--")
	flags = append(flags, positionals...)
	return fs.Parse(flags)
}
func writeJSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
