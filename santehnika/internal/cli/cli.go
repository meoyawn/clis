package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/meoyawn/clis/santehnika/internal/domain"
	"github.com/meoyawn/clis/santehnika/internal/service"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const usage = `santehnika — public catalog in Kazan; JSON output

Usage:
  santehnika search QUERY [--include-filters] [--filter KEY=VALUE]...
  santehnika list CATEGORY_PATH_OR_URL [--include-filters] [--filter KEY=VALUE]...
  santehnika details PRODUCT_URL
  santehnika availability API_ID
  santehnika list --section ID [--filter KEY=VALUE]...
  santehnika auth import [--browser chrome] [--browser-profile Default]
  santehnika auth status

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
Search/list prices are in RUB; detail currency is retained when supplied by the page.
For property filters use properties.code=values.id, such as BRAND=499.
Section lists and search without --include-filters use compact JSON API calls.
The category HTML includes filter definitions without an extra request.
Details include published characteristics, direct image URLs and rating.
rating/review_count describe this exact item, never a variants total.
product_rating/product_review_count retain the same exact-item values.
group_rating/group_review_count hold variant-group aggregates separately.
Unknowns are null; an explicit zero own count stays zero without fallback.
Product HTML only exposes an aggregate; its exact-item review fields are null.
Use search/list for exact-item review counts, without extra requests.
Details expose server-rendered fields; mounting dimensions need the drawing.
This site does not offer a rating sort.
availability uses api_id from search/list and makes one JSON GET for Kazan.
It returns the site's delivery/pickup calculation without fetching product HTML.

No images are fetched automatically. Download selected URLs with curl/wget and
view saved files locally. There is no image subcommand.

Cookie-free requests with ChatGPT-User were rejected during implementation.
Import the existing Chrome session with sweetcookie; import/status are local
and make no HTTP requests. Cookies for this storefront session are saved with
owner-only permissions; cookies from other sites are excluded. Requests use
Chrome-compatible TLS/HTTP2, browser headers and the saved catalog Referer.
Kazan is checked in both session cookies and published page location.
On a protection page or HTTP 401/403/429 the command stops, opens Chrome and
exits with an error. Complete any check manually, import the same profile,
and rerun the command. No automatic retry, challenge solving or cookie refresh.
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
	if command != "search" && command != "list" && command != "details" && command != "availability" && command != "auth" {
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
		fs.StringVar(&section, "section", "", "numeric section ID")
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
	if command == "availability" && fs.NArg() != 1 {
		return fmt.Errorf("availability requires exactly one api_id from search/list")
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
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if command == "auth" {
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
	if len(service.SessionStatus(*sessionPath, s).CookieNames) == 0 {
		return fmt.Errorf("no usable saved cookies; open %s in Chrome, then run go run . auth import --browser chrome", service.Origin)
	}
	if *ua != "" {
		s.UserAgent = *ua
	}
	client, err := service.NewClient(s)
	if err != nil {
		return err
	}
	var result any
	switch command {
	case "search":
		result, err = client.Search(ctx, strings.Join(fs.Args(), " "), o)
	case "list":
		if section != "" {
			result, err = client.Section(ctx, section, o)
		} else {
			result, err = client.List(ctx, fs.Arg(0), o)
		}
	case "details":
		result, err = client.Details(ctx, fs.Arg(0), availability)
	case "availability":
		result, err = client.Availability(ctx, fs.Arg(0))
	}
	if err != nil {
		return err
	}
	if err = service.SaveSession(*sessionPath, client.Session()); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
func reportError(ctx context.Context, err error, stderr io.Writer) {
	fmt.Fprintln(stderr, service.Name+":", err)
	var blocked *service.BlockedError
	if !errors.As(err, &blocked) {
		return
	}
	target := blocked.URL
	if strings.Contains(target, "/ajax/") || strings.Contains(target, "api.lemanapro.ru") {
		target = service.Origin + "/"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", "-a", "Google Chrome", target)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	if openErr := cmd.Run(); openErr != nil {
		fmt.Fprintln(stderr, "Open manually in Chrome:", target)
	} else {
		fmt.Fprintln(stderr, "Opened in Chrome:", target)
	}
	fmt.Fprintln(stderr, "Complete any protection check manually; import the same Chrome profile and rerun the original command.")
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
