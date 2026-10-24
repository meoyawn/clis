package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	ozon "github.com/meoyawn/clis/ozon/internal/service"
	"github.com/steipete/sweetcookie"
)

type dependencies struct {
	client      *ozon.Client
	readCookies ozon.CookieReader
	close       func()
}
type setupFunc func() (dependencies, error)

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return run(ctx, args, stdout, stderr, func() (dependencies, error) {
		store, err := ozon.DefaultAuthStore()
		if err != nil {
			return dependencies{}, err
		}
		h := ozon.NewHTTPClient()
		c, err := ozon.NewClient(h, store)
		return dependencies{client: c, readCookies: sweetcookie.Get, close: h.CloseIdleConnections}, err
	})
}

type filterValues url.Values

func (v filterValues) String() string { return url.Values(v).Encode() }
func (v filterValues) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok || key == "" || value == "" {
		return fmt.Errorf("filter %q must have form KEY=VALUE", raw)
	}
	url.Values(v).Add(key, value)
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, setup setupFunc) error {
	if len(args) == 0 {
		return fmt.Errorf("missing command; %s", usage)
	}
	command := args[0]
	switch command {
	case "help", "--help", "-h":
		_, err := io.WriteString(stdout, usage)
		return err
	case "auth":
		return runAuth(ctx, args[1:], stdout, stderr, setup)
	case "cart":
		return runCart(ctx, args[1:], stdout, stderr, setup)
	case "search", "filters", "show", "reviews":
	default:
		return fmt.Errorf("unknown command %q; %s", command, usage)
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var usageErr error
	flags.Usage = func() { usageErr = printCommandUsage(flags) }
	timeout := flags.Duration("timeout", 60*time.Second, "whole command timeout")
	ua := flags.String("user-agent", "", "HTTP User-Agent")
	page, limit := 1, 0
	var cursor, sort string
	var includeFilters, full bool
	scope := "sku"
	filters := filterValues{}
	if command == "search" || command == "reviews" {
		flags.IntVar(&page, "page", 1, "page number")
		flags.StringVar(&cursor, "cursor", "", "next_cursor from the previous response")
		flags.IntVar(&limit, "limit", 0, "maximum results printed; 0 prints the full page")
	}
	if command == "search" || command == "filters" {
		flags.Var(filters, "filter", "KEY=VALUE; repeatable")
		flags.StringVar(&sort, "sort", "", "Ozon sorting value")
	}
	if command == "reviews" {
		flags.StringVar(&sort, "sort", "usefulness_desc", "usefulness_desc, score_desc or score_asc")
		flags.StringVar(&scope, "scope", "sku", "sku for this variant; all for all variants")
	}
	if command == "search" {
		flags.BoolVar(&includeFilters, "include-filters", false, "include filter and sorting options from the same page")
	}
	if command == "show" {
		flags.BoolVar(&full, "full", false, "also fetch details and cheaper/faster seller offers (at most 4 pages, up to 3 redirects each)")
	}
	if err := parseOptions(flags, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return usageErr
		}
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout %s must be positive", timeout.String())
	}
	if strings.ContainsAny(*ua, "\r\n") {
		return fmt.Errorf("user-agent must not contain newlines")
	}
	if limit < 0 {
		return fmt.Errorf("limit %d must not be negative", limit)
	}
	if cursor != "" {
		var conflicting string
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "page" || f.Name == "filter" || f.Name == "sort" || f.Name == "scope" {
				conflicting = f.Name
			}
		})
		if conflicting != "" {
			return fmt.Errorf("--cursor already includes pagination and filters; do not combine it with --%s", conflicting)
		}
	}
	search := ozon.SearchOptions{Query: strings.TrimSpace(strings.Join(flags.Args(), " ")), Page: page, Cursor: cursor, Filters: url.Values(filters), IncludeFilters: includeFilters}
	var ref ozon.ItemReference
	var reviews ozon.ReviewsOptions
	if command == "search" || command == "filters" {
		if sort != "" {
			search.Filters.Set("sorting", sort)
		}
		if _, err := ozon.SearchPath(search); err != nil {
			return err
		}
	} else {
		if flags.NArg() != 1 {
			return fmt.Errorf("%s expects one SKU or URL, got %d arguments", command, flags.NArg())
		}
		var err error
		ref, err = ozon.ParseItemReference(flags.Arg(0))
		if err != nil {
			return err
		}
		if command == "reviews" {
			reviews.Page = page
			reviews.Cursor = cursor
			reviews.Sort, err = ozon.NewReviewSortFromValue(sort)
			if err != nil {
				return err
			}
			reviews.Scope, err = ozon.NewReviewScopeFromValue(scope)
			if err != nil {
				return err
			}
			if _, err := ozon.ReviewsPath(ref, reviews); err != nil {
				return err
			}
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
	opt := ozon.RequestOptions{UserAgent: *ua}
	switch command {
	case "search":
		result, err := deps.client.Search(ctx, search, opt)
		if err != nil {
			return err
		}
		if limit > 0 && len(result.Results) > limit {
			result.Results = result.Results[:limit]
			result.Count = limit
		}
		return writeJSON(stdout, result)
	case "filters":
		result, err := deps.client.Filters(ctx, search, opt)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "show":
		result, err := deps.client.Show(ctx, ref, ozon.ShowOptions{Full: full}, opt)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "reviews":
		result, err := deps.client.Reviews(ctx, ref, reviews, opt)
		if err != nil {
			return err
		}
		if limit > 0 && len(result.Reviews) > limit {
			result.Reviews = result.Reviews[:limit]
			result.Count = limit
		}
		return writeJSON(stdout, result)
	}
	return nil
}
