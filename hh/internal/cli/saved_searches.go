package cli

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

func runSavedSearches(ctx context.Context, args []string, baseText, profile, cookies string) error {
	if len(args) == 0 || args[0] != "list" && args[0] != "save" {
		return fmt.Errorf("saved-searches requires list or save; run hh --help")
	}
	flags := flag.NewFlagSet("saved-searches "+args[0], flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	output := flags.String("output", "", "JSON output file")
	var search domain.SavedSearchOptions
	var apply bool
	if args[0] == "save" {
		flags.StringVar(&search.Text, "text", "", "native HH query expression")
		flags.BoolVar(&search.Remote, "remote", false, "require REMOTE work format across all locations")
		flags.BoolVar(&apply, "apply", false, "save once and verify native readback")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected saved-searches arguments %q", flags.Args())
	}
	if args[0] == "save" {
		if err := search.Validate(); err != nil {
			return err
		}
	}
	base, err := url.Parse(baseText)
	if err != nil {
		return fmt.Errorf("invalid base URL %q: %w", baseText, err)
	}
	if base.Path != "" && base.Path != "/" || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("invalid base URL %q: use only an origin", baseText)
	}
	base.Path = ""
	unlock, err := lockAccount()
	if err != nil {
		return err
	}
	defer unlock()
	httpClient, err := hh.BrowserClient(ctx, base, profile, cookies)
	if err != nil {
		return err
	}
	client := hh.NewClient(httpClient, base)
	if args[0] == "list" {
		result, err := client.SavedSearches(ctx)
		if err != nil {
			return err
		}
		return outputJSON(*output, result)
	}
	result, err := client.SaveSearch(ctx, search, apply)
	if err != nil {
		return err
	}
	return outputJSON(*output, result)
}
