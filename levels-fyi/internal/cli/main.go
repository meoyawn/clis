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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/meoyawn/clis/levels-fyi/internal/domain"
	"github.com/meoyawn/clis/levels-fyi/internal/service"
)

const help = `Levels.fyi salary research. JSON output, read-only native API.

  auth login [--browser chrome] [--profile Default] [--headers-file FILE]
      SweetCookie import of that Chrome profile, including its actual Cognito
      idToken. Local session file mode 0600; zero network calls.
  auth status
      Local session metadata only; never print cookies or JWTs.
  salaries filters
      Observed native filter semantics, with source and discovery commands.
  search locations --query CITY
      Native city autocomplete, with country IDs; no coordinates exported.
  salaries estimate --role ROLE --level all|junior|middle|senior [--remote]
                    [--country-ids IDS] [--company SLUG]
                    [--job-family software-engineer] [--since YYYY-MM-DD]
                    [--limit 50] [--max-pages 5] [--refresh]
      Example: salaries estimate --role golang --level senior --remote
      No country IDs = all available countries, including US. --remote is
      the native global work-arrangement filter, not a US Remote location.
      Role is native broad text search (cities/tags/etc); software-engineer
      means no text restriction. Job family is a native slug.
      Native levels mean years: junior 0..1, middle 2..6, senior 7+.
      Default window: last 365 days; limit <=50, max-pages <=20, sequential
      requests at least one second apart. Stop immediately on a block.
      Default max-pages 5 covers the inspected website's 250-result cap.
      Cache sanitized reports for 6h. --refresh bypasses it explicitly.
      Return annual USD {amount,currency,interval}; base, stock, bonus and total
      stay separate. Group gross/net and employment type separately; include
      per-country distributions and exact retrieved coverage/source URLs.

An expired saved token triggers one local reimport from the same Chrome
profile before the first API request. A changed account or an expired browser
token stops; open Levels.fyi in that profile, then rerun. No network token
refresh or request retry is performed.

No daemon or cross-process connections. Salary/review/profile writes and
telemetry are not implemented. Authentication/access challenges, redaction,
changed response shapes and rate limits stop without retries or fallback.
Retrieved recent submissions are NOT representative global-market percentiles.
`

func Main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "levels-fyi:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
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
		return fmt.Errorf("expected auth, salaries or search command; run --help")
	}
	command := strings.Join(args[:2], " ")
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var profile, browser, headersFile, role, level, countryIDs, company, family, sinceValue, query string
	var remote, refresh bool
	var limit, pages int
	switch command {
	case "auth login":
		f.StringVar(&profile, "profile", "Default", "Chrome profile")
		f.StringVar(&browser, "browser", "chrome", "browser")
		f.StringVar(&headersFile, "headers-file", "", "safe browser header snapshot")
	case "auth status", "salaries filters":
	case "search locations":
		f.StringVar(&query, "query", "", "native city search")
	case "salaries estimate":
		f.StringVar(&role, "role", "", "native free-text role/tag search")
		f.StringVar(&level, "level", "", "explicit native experience band")
		f.BoolVar(&remote, "remote", false, "native global remote filter")
		f.StringVar(&countryIDs, "country-ids", "", "comma-separated native country IDs")
		f.StringVar(&company, "company", "", "native exact employer slug")
		f.StringVar(&family, "job-family", "software-engineer", "native job family slug")
		f.StringVar(&sinceValue, "since", time.Now().UTC().AddDate(0, 0, -365).Format(time.DateOnly), "lower date bound")
		f.IntVar(&limit, "limit", 50, "rows per native page, <=50")
		f.IntVar(&pages, "max-pages", 5, "maximum sequential pages, <=20")
		f.BoolVar(&refresh, "refresh", false, "bypass local report cache")
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments %v", f.Args())
	}
	var q domain.Query
	var since time.Time
	var err error
	if command == "salaries estimate" {
		if strings.TrimSpace(role) == "" {
			return fmt.Errorf("--role is required")
		}
		lev, err := domain.NewLevelFromValue(level)
		if err != nil {
			return err
		}
		if role == "go" {
			role = "golang"
		}
		if limit < 1 || limit > 50 || pages < 1 || pages > 20 {
			return fmt.Errorf("invalid pagination: limit 1..50 and max-pages 1..20 required")
		}
		if family == "" || strings.ContainsAny(family, "/?#") {
			return fmt.Errorf("invalid job-family slug %q", family)
		}
		since, err = time.Parse(time.DateOnly, sinceValue)
		if err != nil {
			return fmt.Errorf("invalid --since %q: use YYYY-MM-DD", sinceValue)
		}
		q = domain.Query{Role: role, JobFamily: family, Level: lev, Remote: remote, Company: company, Limit: limit, MaxPages: pages, CountryIDs: []int{}}
		if countryIDs != "" {
			for _, part := range strings.Split(countryIDs, ",") {
				id, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil || id <= 0 {
					return fmt.Errorf("invalid --country-ids item %q", part)
				}
				q.CountryIDs = append(q.CountryIDs, id)
			}
		}
	}
	if command == "search locations" && strings.TrimSpace(query) == "" {
		return fmt.Errorf("--query is required")
	}
	if command == "salaries filters" {
		return json.NewEncoder(out).Encode(map[string]any{"levels": map[string]string{"all": "any experience", "junior": "0..1 years", "middle": "2..6 years", "senior": "7+ years"}, "remote": "native workArrangement=remote across all countries unless country IDs restrict it", "country_discovery": "search locations --query CITY; use returned countryId (Kazan -> 203, US -> 254); omit country IDs for global scope", "role_semantics": "native searchText over cities/tags/etc; not a verified language classifier", "salary_units": "annual USD normalized by source; components and gross/net separate", "filter_source": "https://www.levels.fyi/t/software-engineer, native table filters observed 2026-10-25"})
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "levels-fyi")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "command.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("another levels-fyi command is active; wait for it to finish")
	}
	sessionPath := filepath.Join(dir, "session.json")
	if command == "auth login" {
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
	if command == "auth status" {
		s, err := service.LoadSession(sessionPath)
		if errors.Is(err, os.ErrNotExist) {
			return json.NewEncoder(out).Encode(map[string]any{"session_present": false})
		}
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]any{"session_present": true, "profile": s.Profile, "imported_at": s.ImportedAt, "cookie_count": len(s.Cookies), "local_token_usable": service.Authorize(s) == nil, "validated_online": false})
	}
	var cachePath string
	if command == "salaries estimate" {
		key, _ := json.Marshal(struct {
			Query        domain.Query
			Level, Since string
		}{q, q.Level.String(), sinceValue})
		hash := sha256.Sum256(key)
		cacheDir := filepath.Join(dir, "reports")
		if err = os.MkdirAll(cacheDir, 0700); err != nil {
			return err
		}
		cachePath = filepath.Join(cacheDir, hex.EncodeToString(hash[:])+".json")
		if !refresh {
			if b, err := os.ReadFile(cachePath); err == nil {
				var saved domain.Report
				if json.Unmarshal(b, &saved) == nil && time.Since(saved.FetchedAt) >= 0 && time.Since(saved.FetchedAt) < 6*time.Hour {
					saved.Requests = 0
					saved.Limitations = append(saved.Limitations, "Read from local cache; fetched_at retains the source retrieval time.")
					return json.NewEncoder(out).Encode(saved)
				}
			}
		}
	}
	session, err := service.LoadAuthorizedSession(ctx, sessionPath)
	if err != nil {
		return fmt.Errorf("load session: %w; run auth login --browser chrome", err)
	}
	httpClient, headers, err := service.HTTPClient(session)
	if err != nil {
		return err
	}
	defer httpClient.CloseIdleConnections()
	client, err := service.NewClient(httpClient, headers)
	if err != nil {
		return err
	}
	if command == "search locations" {
		locations, err := client.Locations(ctx, query)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(locations)
	}
	report, err := client.Estimate(ctx, q, since)
	if err != nil {
		return err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	if err = os.WriteFile(cachePath, data, 0600); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}
