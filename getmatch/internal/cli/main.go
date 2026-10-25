package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/meoyawn/clis/getmatch/internal/domain"
	"github.com/meoyawn/clis/getmatch/internal/service"
)

const help = `Getmatch salary research, as JSON. Read-only; no profile/salary submissions.

Usage: getmatch [--anonymous] COMMAND [OPTIONS]

  salaries filters [--refresh]
      Fetch live specialization catalog; list observed level/city/work filters.
      Cache the public catalog for 24h. --refresh replaces that cache.
  salaries estimate --role SLUG --level LEVEL [--remote] [--city CITY]
                    [--work-format FORMAT] [--company NAME]
                    [--hours-per-month NUMBER] [--refresh]
      --role aliases --specialization; --remote aliases --work-format remote.
      One reproducible market report: percentiles, report count, histogram,
      separate annual bonus, exact-name match within returned top companies.
      --level is required: all, junior, middle, senior, lead, c_level.
      --city: all (default), moscow, spb, other_russia.
      --work-format: all (default), remote, office_hybrid.
      Company is a local match in a TOP ranking, never an employer budget.
      Explicit hours produce an arithmetic equivalent, not a contractor quote.
  auth login [--browser chrome] [--profile Default] [--headers-file FILE]
      Import one real Chrome profile with SweetCookie. Save session mode 0600.
      Optional file: safe User-Agent/Accept-Language/Sec-CH-UA headers captured
      from that same session; otherwise derive UA from installed macOS Chrome.
  auth status
      Local cookie presence/expiry metadata only; never print credentials.

Use --anonymous before COMMAND to avoid loading a saved session. With no saved
session the public endpoints are used anonymously. A blocked/partial report
fails instead of importing/retrying automatically. Each invocation is one
foreground process: no daemon, no connection reuse between processes.
Errors go to stderr. Stop on 403/429, login, checkpoint or changed API shape.
No automatic retries. Source currency/period and unknown gross/net stay explicit.
GETs: /api/specializations (cached), /api/salaries and /api/salaries/stats.
The API's personal salary/profile fields are not included in output or caches.
`

func Main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "getmatch:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	global := flag.NewFlagSet("getmatch", flag.ContinueOnError)
	anon := global.Bool("anonymous", false, "do not load saved session")
	global.SetOutput(io.Discard)
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(out, help)
			return err
		}
		return err
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" {
		_, err := io.WriteString(out, help)
		return err
	}
	for _, a := range args {
		if a == "--help" || a == "-h" {
			_, err := io.WriteString(out, help)
			return err
		}
	}
	if len(args) < 2 {
		return fmt.Errorf("expected salaries filters, salaries estimate, auth login or auth status")
	}
	known := args[0] == "salaries" && (args[1] == "filters" || args[1] == "estimate") || args[0] == "auth" && (args[1] == "login" || args[1] == "status")
	if !known {
		return fmt.Errorf("unknown command %q; run --help", strings.Join(args[:2], " "))
	}
	flags := flag.NewFlagSet(strings.Join(args[:2], " "), flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var profile, browser, headersFile, slug, level, city, work, company string
	var refresh, remote bool
	var role string
	var hours float64
	if args[0] == "auth" && args[1] == "login" {
		flags.StringVar(&profile, "profile", "Default", "Chrome profile")
		flags.StringVar(&browser, "browser", "chrome", "browser")
		flags.StringVar(&headersFile, "headers-file", "", "safe native browser headers")
	} else if args[0] == "salaries" {
		flags.BoolVar(&refresh, "refresh", false, "refresh public catalog")
		if args[1] == "estimate" {
			flags.StringVar(&slug, "specialization", "", "live specialization slug")
			flags.StringVar(&role, "role", "", "specialization slug, alias for --specialization")
			flags.BoolVar(&remote, "remote", false, "shorthand for --work-format remote")
			flags.StringVar(&level, "level", "", "explicit experience level")
			flags.StringVar(&city, "city", "all", "residence cohort")
			flags.StringVar(&work, "work-format", "all", "work format cohort")
			flags.StringVar(&company, "company", "", "exact company name in top ranking")
			flags.Float64Var(&hours, "hours-per-month", 0, "explicit normalization assumption")
		}
	}
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	var lev domain.Level
	var loc domain.City
	var wf domain.WorkFormat
	var err error
	if args[0] == "salaries" && args[1] == "estimate" {
		if role != "" {
			if strings.EqualFold(role, "go") {
				role = "golang"
			}
			if slug != "" && slug != role {
				return fmt.Errorf("--role and --specialization disagree")
			}
			slug = role
		}
		if remote {
			if work != "all" && work != "remote" {
				return fmt.Errorf("--remote conflicts with --work-format %q", work)
			}
			work = "remote"
		}
		if hours < 0 || math.IsNaN(hours) || math.IsInf(hours, 0) {
			return fmt.Errorf("invalid --hours-per-month %v", hours)
		}
		if slug == "" {
			return fmt.Errorf("--specialization is required; discover salaries filters")
		}
		if lev, err = domain.NewLevelFromValue(level); err != nil {
			return err
		}
		if loc, err = domain.NewCityFromValue(city); err != nil {
			return err
		}
		if wf, err = domain.NewWorkFormatFromValue(work); err != nil {
			return err
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
	path := filepath.Join(dir, "session.json")
	if args[0] == "auth" {
		if args[1] == "login" {
			if browser != "chrome" {
				return fmt.Errorf("unsupported browser %q: use chrome", browser)
			}
			s, err := service.ImportSession(ctx, profile, headersFile)
			if err != nil {
				return err
			}
			if err = service.SaveSession(path, s); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(map[string]any{"imported": true, "profile": s.Profile, "cookie_count": len(s.Cookies), "path": path, "network_requests": 0})
		}
		s, err := service.LoadSession(path)
		if errors.Is(err, os.ErrNotExist) {
			return json.NewEncoder(out).Encode(map[string]any{"session_present": false, "validated_online": false})
		}
		if err != nil {
			return err
		}
		expired := 0
		for _, c := range s.Cookies {
			if !c.Expires.IsZero() && c.Expires.Before(time.Now()) {
				expired++
			}
		}
		return json.NewEncoder(out).Encode(map[string]any{"session_present": true, "profile": s.Profile, "imported_at": s.ImportedAt, "cookies": len(s.Cookies), "expired_cookies": expired, "validated_online": false})
	}
	var session *service.Session
	if !*anon {
		session, err = service.LoadSession(path)
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
	client := service.NewClient(httpClient, headers)
	catalog, err := loadCatalog(ctx, client, filepath.Join(dir, "specializations.json"), refresh)
	if err != nil {
		return err
	}
	if args[1] == "filters" {
		return json.NewEncoder(out).Encode(map[string]any{"specializations": catalog.Values, "specializations_fetched_at": catalog.FetchedAt, "source_url": service.Origin + "/api/specializations", "levels": []string{"all", "junior", "middle", "senior", "lead", "c_level"}, "cities": []string{"all", "moscow", "spb", "other_russia"}, "work_formats": []string{"all", "remote", "office_hybrid"}, "static_filter_source": "observed native salary UI and request parameters, 2026-10-25"})
	}
	var selected *domain.Specialization
	for _, s := range catalog.Values {
		if s.Slug == slug {
			v := s
			selected = &v
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("unknown specialization %q; run salaries filters --refresh", slug)
	}
	report, err := client.Estimate(ctx, domain.Query{Specialization: *selected, Level: lev, City: loc, WorkFormat: wf}, company, hours)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}

type catalogCache struct {
	FetchedAt time.Time               `json:"fetched_at"`
	Values    []domain.Specialization `json:"values"`
}

func loadCatalog(ctx context.Context, c *service.Client, path string, refresh bool) (catalogCache, error) {
	if !refresh {
		data, err := os.ReadFile(path)
		if err == nil {
			var cached catalogCache
			if json.Unmarshal(data, &cached) == nil && len(cached.Values) > 0 && time.Since(cached.FetchedAt) >= 0 && time.Since(cached.FetchedAt) < 24*time.Hour {
				return cached, nil
			}
		}
	}
	values, err := c.Specializations(ctx)
	if err != nil {
		return catalogCache{}, err
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Slug < values[j].Slug })
	cached := catalogCache{time.Now().UTC(), values}
	data, err := json.Marshal(cached)
	if err != nil {
		return catalogCache{}, err
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		return catalogCache{}, err
	}
	return cached, nil
}
