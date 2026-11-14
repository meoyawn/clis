package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"

	"github.com/meoyawn/clis/getmatch/internal/domain"
	"github.com/meoyawn/clis/getmatch/internal/service"
)

type searchValues []string

func (v *searchValues) String() string { return fmt.Sprint([]string(*v)) }

func (v *searchValues) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func parseVacancySearch(args []string) (domain.VacancySearchOptions, string, error) {
	var search domain.VacancySearchOptions
	var levels searchValues
	f := flag.NewFlagSet("vacancies search", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.Var((*searchValues)(&search.Specializations), "specialization", "native specialization slug; repeat for multiple fields")
	f.Var((*searchValues)(&search.Skills), "skill", "exact key-skill tag; repeat to match any requested tag")
	f.Var(&levels, "level", "junior, middle, senior, lead or c_level; repeat for multiple levels")
	f.BoolVar(&search.Remote, "remote", false, "require an included remote work location")
	f.IntVar(&search.Salary, "salary", 0, "native monthly RUB threshold: 0, 150000, 200000, 250000 or 350000")
	f.IntVar(&search.Page, "page", 0, "zero-based candidate page")
	f.IntVar(&search.PerPage, "per-page", 20, "candidate page size, 1 to 100")
	output := f.String("output", "", "new private JSON output file")
	if err := f.Parse(args); err != nil {
		return search, "", err
	}
	if f.NArg() != 0 {
		return search, "", fmt.Errorf("unexpected search arguments %q", f.Args())
	}
	for _, value := range levels {
		level, err := domain.NewLevelFromValue(value)
		if err != nil {
			return search, "", err
		}
		search.Levels = append(search.Levels, level)
	}
	return search, *output, search.Validate()
}

func runVacancySearch(ctx context.Context, args []string, anonymous bool, out io.Writer) error {
	search, output, err := parseVacancySearch(args)
	if err != nil {
		return err
	}
	if output != "" {
		if _, err := os.Stat(output); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("output %q must be a new private file", output)
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "getmatch")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "command.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another getmatch command is active; wait for it to finish")
	}
	var session *service.Session
	if !anonymous {
		session, err = service.LoadSession(filepath.Join(dir, "session.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	httpClient, headers, err := service.HTTPClient(session)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	if len(headers) == 0 {
		headers, err = service.ChromeHeaders(ctx)
		if err != nil {
			headers = http.Header{"User-Agent": {"getmatch-cli/0.1"}}
		}
	}
	result, err := service.NewClient(httpClient, headers).SearchVacancies(ctx, search)
	if err != nil {
		return err
	}
	return privateJSON(output, result, out)
}
