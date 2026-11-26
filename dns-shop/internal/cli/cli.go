package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/meoyawn/clis/dns-shop/internal/domain"
	"github.com/meoyawn/clis/dns-shop/internal/service"
)

const usage = `dns-shop — public DNS (dns-shop.ru) catalog; JSON output

Usage:
  dns-shop search QUERY [--category ID] [--filter KEY=VALUE]... [--include-filters]
  dns-shop details PRODUCT_URL_OR_CODE
  dns-shop auth import [--browser chrome] [--browser-profile Default]
  dns-shop auth status

Options:
  --city SLUG           Required storefront city, as in /shops/SLUG/ (default kazan)
  --page N              One server page of 24 products, starting at 1 (default 1)
  --limit N             Trim this page locally; 0 prints all its products
  --sort ORDER          popular (default), price-asc, price-desc, new, opinion
                        (most reviews), rating
  --category ID         Category ID from the "category" filter
  --filter KEY=VALUE    Repeat with keys/value IDs from --include-filters
  --include-filters     Add one request for the current filters and counts
  --timeout 60s         Whole-command timeout
  --user-agent TEXT     Override the saved browser User-Agent
  --session-file PATH   Override the owner-only local session file
  --json                Accepted; JSON is always the output format

Search makes one GET for the page and one POST for its prices. A query without
--category is redirected by the site to its best-matching category; the result
url shows it, and the "category" filter lists the other matching categories.
That redirect drops sort, page and filters, so without --category they cost
one more GET; pass --category from a previous result to avoid it.
Filters: repeated values of one key are joined with the site's "-" separator
(--filter brand=aceline --filter brand=dexp). Toggles use KEY=1, ranges use
price=MIN-MAX, stock uses now/today/tomorrow/later/out_of_stock.
has_more says whether a next page exists; total is the site's match count.

The site prices and stocks pages for the city saved in a server-signed cookie,
which only the browser can change. Commands refuse to run unless the imported
session's city matches --city; to use another city, select it on dns-shop.ru in
Chrome, run auth import, and pass --city with its slug. city in the output is
the session city's display name.

price/old_price are RUB for the city. rating/review_count are what the site
shows on the card; colour variants of one model often share them.
reliability is the site's service-rating label. availability is the card's
status (now, today, tomorrow, later, out_of_stock) or, in details, the
schema.org value (InStock, OutOfStock, ...).

details makes two requests: grouped characteristics with the description,
then price, rating and up to three image URLs. A numeric product code adds one
search request to resolve the product URL.

No images are fetched automatically. Download selected URLs with curl/wget and
view saved files locally. There is no image subcommand.

Requests without browser cookies are rejected by Qrator protection.
Import the existing Chrome session with sweetcookie; import/status are local
and make no HTTP requests. Only protection, city and language cookies are
saved, with owner-only permissions; account, cart and analytics cookies are
excluded. When the protection cookie is stale, or a request hits a protection
page or HTTP 401/403/429, the command re-reads Chrome's cookies, opening the
storefront in a background Chrome tab so Chrome renews them, and retries once.
If Chrome has no fresh cookie within 90s, it brings Chrome to the front and
exits; the human completes the check and reruns.
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
	if command != "search" && command != "details" && command != "auth" {
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeout := fs.Duration("timeout", 60*time.Second, "whole-command timeout")
	ua := fs.String("user-agent", "", "HTTP User-Agent")
	sessionPath := fs.String("session-file", "", "local session file")
	city := fs.String("city", service.DefaultCity, "storefront city slug")
	fs.Bool("json", false, "JSON output")
	var o service.Options
	var filters repeated
	var sortValue string
	if command == "search" {
		fs.IntVar(&o.Page, "page", 1, "server page")
		fs.IntVar(&o.Limit, "limit", 0, "local output limit")
		fs.BoolVar(&o.IncludeFilters, "include-filters", false, "include current filters")
		fs.StringVar(&o.Category, "category", "", "category ID")
		fs.StringVar(&sortValue, "sort", "popular", "sort order")
		fs.Var(&filters, "filter", "KEY=VALUE")
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
	if !service.ValidCity(*city) {
		return fmt.Errorf("city must be a lowercase slug such as kazan")
	}
	if command == "search" {
		var err error
		if o.Sort, err = domain.NewSort(sortValue); err != nil {
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
		if err = o.Validate(); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return fmt.Errorf("search requires a query")
		}
	}
	if command == "details" && fs.NArg() != 1 {
		return fmt.Errorf("details requires exactly one product URL or code")
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
	if err = service.RequireCity(s, *city); err != nil {
		return err
	}
	profile = s.Profile
	if profile == "" {
		profile = "Default"
	}
	if service.FreshProtection(s, protectionMargin) == "" {
		if s, err = refreshSession(ctx, *sessionPath, profile, s.UserAgent, service.Origin+"/", ""); err != nil {
			return err
		}
	}
	fetch := func(s service.Session) (any, *service.Client, error) {
		if *ua != "" {
			s.UserAgent = *ua
		}
		if err := service.RequireCity(s, *city); err != nil {
			return nil, nil, err
		}
		client, err := service.NewClient(s)
		if err != nil {
			return nil, nil, err
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if command == "search" {
			r, err := client.Search(ctx, strings.Join(fs.Args(), " "), o)
			return r, client, err
		}
		r, err := client.Details(ctx, fs.Arg(0))
		return r, client, err
	}
	result, client, err := fetch(s)
	var blocked *service.BlockedError
	if errors.As(err, &blocked) {
		if s, err = refreshSession(ctx, *sessionPath, profile, s.UserAgent, chromeTarget(blocked.URL), service.FreshProtection(s, 0)); err != nil {
			return err
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

const (
	protectionMargin = 30 * time.Second
	refreshWait      = 90 * time.Second
)

// refreshSession re-imports Chrome's cookies until it holds a protection cookie
// other than stale. Chrome renews the cookie only when it loads the storefront,
// so a background tab is opened once if the first import is not fresh.
func refreshSession(ctx context.Context, path, profile, ua, target, stale string) (service.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, refreshWait)
	defer cancel()
	opened := false
	for {
		if _, err := service.ImportSession(ctx, path, profile, ua); err == nil {
			if s, err := service.LoadSession(path); err == nil {
				if v := service.FreshProtection(s, protectionMargin); v != "" && v != stale {
					return s, nil
				}
			}
		}
		if !opened {
			if err := openChrome(ctx, target, true); err != nil {
				return service.Session{}, fmt.Errorf("open Chrome to renew the catalog session: %w", err)
			}
			opened = true
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return service.Session{}, &service.BlockedError{URL: target}
			}
			return service.Session{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// chromeTarget maps JSON endpoints to a page Chrome can render.
func chromeTarget(target string) string {
	if strings.Contains(target, "/ajax-state/") || strings.Contains(target, "/microdata/") || strings.Contains(target, "/characteristics/") || strings.Contains(target, "/filters/") {
		return service.Origin + "/"
	}
	return target
}

func openChrome(ctx context.Context, target string, background bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		args := []string{"-a", "Google Chrome", target}
		if background {
			args = append([]string{"-g"}, args...)
		}
		cmd = exec.CommandContext(ctx, "open", args...)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	return cmd.Run()
}

func reportError(ctx context.Context, err error, stderr io.Writer) {
	fmt.Fprintln(stderr, service.Name+":", err)
	var blocked *service.BlockedError
	if !errors.As(err, &blocked) {
		return
	}
	target := chromeTarget(blocked.URL)
	if openErr := openChrome(ctx, target, false); openErr != nil {
		fmt.Fprintln(stderr, "Open manually in Chrome:", target)
	} else {
		fmt.Fprintln(stderr, "Opened in Chrome:", target)
	}
	fmt.Fprintln(stderr, "Complete the check manually, then rerun the original command.")
}

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
