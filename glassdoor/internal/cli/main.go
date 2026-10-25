package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/meoyawn/clis/glassdoor/internal/domain"
	"github.com/meoyawn/clis/glassdoor/internal/service"
)

const help = `Glassdoor salary research, as JSON. Read-only native requests.

Usage: glassdoor [--anonymous] COMMAND [OPTIONS]

  auth login [--browser chrome] [--profile Default] [--headers-file FILE]
      Import one Chrome profile with SweetCookie, session file mode 0600.
      Use a safe native browser header snapshot for the same Chrome session;
      otherwise derive headers from installed macOS Chrome. No network calls.
  auth status
      Local session-presence/expiry metadata, not online authentication proof.
  search titles --query TEXT [--refresh]
  search locations --query TEXT [--refresh]
  search companies --query TEXT [--refresh]
      Native autocomplete suggestions. IDs distinguish similarly named places
      and employers; a suggestion is not proof of employer identity or pay.
  salaries filters [--refresh]
      Native experience values and industry catalog from the salary page.
  salaries estimate --role ROLE --level LEVEL --location LOCATION
      Example: salaries estimate --role golang --level senior --location Russia
      Alternative: --title EXACT_NATIVE_TITLE (without --role/--level)
                    [--title-id ID] [--location-id ID]
                    [--experience all|0-1|1-3|4-6|7-9|10-14|15+]
                    [--industry-id ID] [--refresh]
      Resolve exact title/location through native autocomplete, then one
      read-only occ-salary-estimates POST. Optional IDs must match suggestions.
      Each percentile is {amount, currency, interval}; preserve
      base/additional/total pay separately, native period/currency,
      sample size, provider confidence and source update timestamp.
      --remote requires explicit --location Remote (United States); it never
      substitutes that market for worldwide remote.
      --location is required; no city/country is inferred. Unsupported or ambiguous locations stop.
      Industry 0 means all; nonzero IDs are checked against the live catalog.

Queries/catalogs are cached for 24h; salary estimates are fetched fresh.
Default: saved Chrome session required. --anonymous is explicit, never a
fallback after a block. Stop on 403/429, login or challenge; no automatic retry.
No salary/review/profile submissions and no telemetry mutations. No daemon:
HTTP connection reuse lasts only for one foreground invocation.
Errors go to stderr; cookies, personal account data and tokens never go to stdout.
`

func Main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "glassdoor:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	g := flag.NewFlagSet("glassdoor", flag.ContinueOnError)
	g.SetOutput(io.Discard)
	anonymous := g.Bool("anonymous", false, "explicit unauthenticated access")
	if err := g.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(out, help)
			return err
		}
		return err
	}
	args = g.Args()
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
		return fmt.Errorf("expected auth, search or salaries command; run --help")
	}
	valid := args[0] == "auth" && (args[1] == "login" || args[1] == "status") || args[0] == "search" && (args[1] == "titles" || args[1] == "locations" || args[1] == "companies") || args[0] == "salaries" && (args[1] == "filters" || args[1] == "estimate")
	if !valid {
		return fmt.Errorf("unknown command %q", strings.Join(args[:2], " "))
	}
	f := flag.NewFlagSet(strings.Join(args[:2], " "), flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var profile, browser, headersFile, query, title, location, experience string
	var refresh, remote bool
	var role, level string
	var titleID, locationID, industryID int
	if args[0] == "auth" && args[1] == "login" {
		f.StringVar(&profile, "profile", "Default", "Chrome profile")
		f.StringVar(&browser, "browser", "chrome", "browser")
		f.StringVar(&headersFile, "headers-file", "", "safe native header snapshot")
	}
	if args[0] == "search" {
		f.StringVar(&query, "query", "", "native autocomplete query")
		f.BoolVar(&refresh, "refresh", false, "refresh lookup cache")
	}
	if args[0] == "salaries" {
		f.BoolVar(&refresh, "refresh", false, "refresh catalog cache")
		if args[1] == "estimate" {
			f.StringVar(&title, "title", "", "exact job title")
			f.StringVar(&role, "role", "", "role alias or native title")
			f.StringVar(&level, "level", "all", "title seniority: all, junior, middle, senior, lead")
			f.BoolVar(&remote, "remote", false, "native Remote location, United States; not worldwide")
			f.StringVar(&location, "location", "", "exact location")
			f.IntVar(&titleID, "title-id", 0, "disambiguating suggestion ID")
			f.IntVar(&locationID, "location-id", 0, "disambiguating suggestion ID")
			f.StringVar(&experience, "experience", "all", "experience band")
			f.IntVar(&industryID, "industry-id", 0, "native industry ID")
		}
	}
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	if f.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	var exp domain.Experience
	var err error
	if args[0] == "search" && strings.TrimSpace(query) == "" {
		return fmt.Errorf("--query is required")
	}
	if args[0] == "salaries" && args[1] == "estimate" {
		if title != "" && role != "" {
			return fmt.Errorf("use --role or --title, not both")
		}
		lev, parseErr := domain.NewLevelFromValue(level)
		if parseErr != nil {
			return parseErr
		}
		if role != "" {
			title, err = domain.TitleForRole(role, lev)
			if err != nil {
				return err
			}
		} else if lev != domain.AllLevels() {
			return fmt.Errorf("--level applies to --role; --title is already the exact native title")
		}
		if remote && !strings.EqualFold(location, "Remote") {
			return fmt.Errorf("glassdoor has no verified global remote cohort in this API; native Remote is United States only. Use an explicit --location Remote for that market, or levels-fyi for worldwide remote evidence")
		}
		if title == "" || location == "" {
			return fmt.Errorf("--title and --location are required; no geographic default is assumed")
		}
		if titleID < 0 || locationID < 0 || industryID < 0 {
			return fmt.Errorf("IDs must be nonnegative")
		}
		if exp, err = domain.NewExperienceFromValue(experience); err != nil {
			return err
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "glassdoor")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "command.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another glassdoor command is active; wait for it to finish")
	}
	sessionPath := filepath.Join(dir, "session.json")
	if args[0] == "auth" {
		if args[1] == "login" {
			if browser != "chrome" {
				return fmt.Errorf("unsupported browser %q: use chrome", browser)
			}
			s, err := service.ImportSession(ctx, profile, headersFile)
			if err != nil {
				return err
			}
			if err = service.SaveSession(sessionPath, s); err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(map[string]any{"imported": true, "profile": s.Profile, "cookie_count": len(s.Cookies), "path": sessionPath, "network_requests": 0})
		}
		s, err := service.LoadSession(sessionPath)
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
	if !*anonymous {
		session, err = service.LoadSession(sessionPath)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no saved Glassdoor session; run auth login --browser chrome before a network request")
		}
		if err != nil {
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
			return err
		}
	}
	client := service.NewClient(httpClient, headers)
	cacheDir := filepath.Join(dir, "catalog")
	if err = os.MkdirAll(cacheDir, 0700); err != nil {
		return err
	}
	if args[0] == "search" {
		switch args[1] {
		case "titles":
			v, err := cached(ctx, cacheDir, "titles:"+query, refresh, func() ([]domain.Title, error) { return client.Titles(ctx, query) })
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(v)
		case "locations":
			v, err := cached(ctx, cacheDir, "locations:"+query, refresh, func() ([]domain.Location, error) { return client.Locations(ctx, query) })
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(v)
		case "companies":
			v, err := cached(ctx, cacheDir, "companies:"+query, refresh, func() ([]domain.Employer, error) { return client.Employers(ctx, query) })
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(v)
		}
	}
	if args[1] == "filters" {
		industries, err := cached(ctx, cacheDir, "industries", refresh, func() ([]domain.Industry, error) { return client.Industries(ctx) })
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"experience_aliases": []string{"all", "0-1", "1-3", "4-6", "7-9", "10-14", "15+"}, "industries": industries, "all_industries_id": 0, "industry_source_url": service.FilterSourceURL, "location_discovery": "search locations --query TEXT", "title_discovery": "search titles --query TEXT"})
	}
	titles, err := cached(ctx, cacheDir, "titles:"+title, refresh, func() ([]domain.Title, error) { return client.Titles(ctx, title) })
	if err != nil {
		return err
	}
	var chosenTitles []domain.Title
	for _, v := range titles {
		if titleID > 0 && v.ID == titleID || titleID == 0 && (strings.EqualFold(v.JobTitle, title) || strings.EqualFold(v.Label, title)) {
			chosenTitles = append(chosenTitles, v)
		}
	}
	if len(chosenTitles) != 1 {
		data, _ := json.Marshal(titles)
		return fmt.Errorf("title %q is missing or ambiguous; select --title-id from these suggestions: %s", title, data)
	}
	locations, err := cached(ctx, cacheDir, "locations:"+location, refresh, func() ([]domain.Location, error) { return client.Locations(ctx, location) })
	if err != nil {
		return err
	}
	var chosenLocations []domain.Location
	for _, v := range locations {
		if locationID > 0 && v.LocationID == locationID || locationID == 0 && (strings.EqualFold(v.Name, location) || strings.EqualFold(v.Label, location) || strings.EqualFold(v.LongName, location)) {
			chosenLocations = append(chosenLocations, v)
		}
	}
	if len(chosenLocations) != 1 {
		data, _ := json.Marshal(locations)
		return fmt.Errorf("location %q is missing or ambiguous; select --location-id from these suggestions: %s", location, data)
	}
	industry := domain.Industry{ID: 0, Name: "All industries"}
	if industryID > 0 {
		values, err := cached(ctx, cacheDir, "industries", refresh, func() ([]domain.Industry, error) { return client.Industries(ctx) })
		if err != nil {
			return err
		}
		found := false
		for _, v := range values {
			if v.ID == industryID {
				industry = v
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("industry ID %d is absent from the native catalog; run salaries filters --refresh", industryID)
		}
	}
	report, err := client.Estimate(ctx, domain.Query{Title: chosenTitles[0], Location: chosenLocations[0], Experience: exp, Industry: industry})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}

type cacheEnvelope[T any] struct {
	FetchedAt time.Time `json:"fetched_at"`
	Value     T         `json:"value"`
}

func cached[T any](ctx context.Context, dir, key string, refresh bool, fetch func() (T, error)) (T, error) {
	var empty T
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	sum := sha256.Sum256([]byte(key))
	path := filepath.Join(dir, hex.EncodeToString(sum[:])+".json")
	if !refresh {
		if data, err := os.ReadFile(path); err == nil {
			var saved cacheEnvelope[T]
			if json.Unmarshal(data, &saved) == nil && time.Since(saved.FetchedAt) >= 0 && time.Since(saved.FetchedAt) < 24*time.Hour {
				return saved.Value, nil
			}
		}
	}
	value, err := fetch()
	if err != nil {
		return empty, err
	}
	data, err := json.Marshal(cacheEnvelope[T]{time.Now().UTC(), value})
	if err != nil {
		return empty, err
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		return empty, err
	}
	return value, nil
}
