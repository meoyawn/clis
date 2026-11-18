package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/meoyawn/clis/hh/internal/domain"
	hh "github.com/meoyawn/clis/hh/internal/service"
)

type searchValues []string

func (v *searchValues) String() string { return fmt.Sprint([]string(*v)) }

func (v *searchValues) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func parseVacancySearch(args []string) (domain.VacancySearchOptions, string, error) {
	var search domain.VacancySearchOptions
	options := flag.NewFlagSet("vacancies search", flag.ContinueOnError)
	options.SetOutput(os.Stderr)
	options.StringVar(&search.Text, "text", "", "native HH query expression for candidate discovery")
	options.Var((*searchValues)(&search.Skills), "skill", "exact key-skill tag; repeat to match any requested tag")
	options.BoolVar(&search.Remote, "remote", false, "require REMOTE work format")
	options.Var((*searchValues)(&search.Areas), "area", "HH area ID; repeat for multiple areas, omit for all locations")
	options.Var((*searchValues)(&search.Employers), "employer", "HH employer ID; repeat for multiple employers")
	options.StringVar(&search.ProfessionalArea, "professional-area", "", "HH professional category ID; resolve its current roles from the native search dictionary")
	options.IntVar(&search.Page, "page", 0, "zero-based candidate page")
	options.IntVar(&search.PerPage, "per-page", 20, "candidate page size, 1 to 100")
	output := options.String("output", "", "JSON output file")
	if err := options.Parse(args); err != nil {
		return search, "", err
	}
	if options.NArg() != 0 {
		return search, "", fmt.Errorf("unexpected search arguments %q", options.Args())
	}
	return search, *output, search.Validate()
}

func runVacancySearch(ctx context.Context, args []string, baseText, profile, cookies string, headers http.Header) error {
	search, output, err := parseVacancySearch(args)
	if err != nil {
		return err
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
	client, err := hh.BrowserClient(ctx, base, profile, cookies, headers)
	if err != nil {
		return err
	}
	result, err := client.SearchVacancies(ctx, search)
	if err != nil {
		return err
	}
	return outputJSON(output, result)
}
