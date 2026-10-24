package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meoyawn/clis/uslugi-tatar/internal/domain"
	"github.com/steipete/sweetcookie"
	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

var errNotAuthenticated = errors.New("portal session is not authenticated; log in in your browser, then run auth import --browser <browser>")

// Only portal cookies and safe source metadata are persisted, never the browser DB.
type savedSession struct {
	Version    int            `json:"version"`
	Browser    string         `json:"browser"`
	Profile    string         `json:"profile,omitempty"`
	ImportedAt time.Time      `json:"imported_at"`
	Cookies    []*http.Cookie `json:"cookies"`
}

func authFile() (string, error) {
	dir := os.Getenv("USLUGI_TATAR_CONFIG_DIR")
	if dir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user config directory: %w", err)
		}
		dir = filepath.Join(root, "uslugi-tatar-cli")
	}
	return filepath.Abs(filepath.Join(dir, "auth.json"))
}

func saveSession(path string, session savedSession) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("protect auth directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".auth-*")
	if err != nil {
		return fmt.Errorf("create private auth file: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(session); err != nil {
		return fmt.Errorf("encode auth file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync auth file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close auth file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("save auth file: %w", err)
	}
	return nil
}

func loadSession(path string) (savedSession, error) {
	var session savedSession
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return session, fmt.Errorf("no imported session at %s; run auth import --browser <browser>", path)
	}
	if err != nil {
		return session, fmt.Errorf("inspect auth file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return session, fmt.Errorf("auth file %s must be a regular private file (chmod 600)", path)
	}
	if info.Size() > 1<<20 {
		return session, fmt.Errorf("auth file is too large; reimport the browser session")
	}
	f, err := os.Open(path)
	if err != nil {
		return session, fmt.Errorf("open auth file: %w", err)
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&session); err != nil {
		// Decoder errors may contain secret field names or cookie contents.
		return savedSession{}, fmt.Errorf("invalid auth file; reimport the browser session")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) || session.Version != 1 {
		return savedSession{}, fmt.Errorf("invalid auth file version or trailing data; reimport the browser session")
	}
	if _, err := domain.NewBrowserFromValue(session.Browser); err != nil {
		return savedSession{}, fmt.Errorf("invalid browser in auth file; reimport the browser session")
	}
	return session, nil
}

func sessionClient(session savedSession) (*portalClient, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, fmt.Errorf("create session jar: %w", err)
	}
	origin, _ := url.Parse(portalURL)
	for _, cookie := range session.Cookies {
		if cookie == nil || cookie.Valid() != nil {
			return nil, fmt.Errorf("invalid cookie in auth file; reimport the browser session")
		}
		domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
		if domain != origin.Hostname() && domain != "tatarstan.ru" {
			return nil, fmt.Errorf("auth file contains a cookie outside the portal domain; reimport the browser session")
		}
		jar.SetCookies(origin, []*http.Cookie{cookie})
	}
	if len(jar.Cookies(origin)) == 0 {
		return nil, fmt.Errorf("no unexpired portal session cookies; run auth import --browser <browser>")
	}
	return newPortalClient(portalURL, &http.Client{Jar: jar, Timeout: 30 * time.Second})
}

func authenticatedClient() (*portalClient, error) {
	path, err := authFile()
	if err != nil {
		return nil, err
	}
	session, err := loadSession(path)
	if err != nil {
		return nil, err
	}
	return sessionClient(session)
}

func (c *portalClient) checkAuth(ctx context.Context) error {
	p, err := c.request(ctx, c.base.ResolveReference(&url.URL{Path: "/"}), nil)
	if err != nil {
		return err
	}
	// The login popup exists even when signed in. Only the signed-in menu has
	// a same-origin logout link; this check never follows it.
	for _, node := range find(p.doc, func(n *html.Node) bool { return n.Data == "a" }) {
		u, err := p.url.Parse(attr(node, "href"))
		if err == nil && u.Path == "/user/logout" && c.checkOrigin(u) == nil {
			return nil
		}
	}
	return errNotAuthenticated
}

func runAuth(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(out, "Usage: auth status | auth import --browser <browser> [--profile <profile>]")
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: auth status | auth import --browser <browser> [--profile <profile>]")
	}
	if args[0] != "status" && args[0] != "import" {
		return fmt.Errorf("unknown auth command %q; expected status or import", args[0])
	}
	flags := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	var browser, profile string
	if args[0] == "import" {
		flags.StringVar(&browser, "browser", "", "browser to read, e.g. chrome, firefox, safari, brave, arc, edge")
		flags.StringVar(&profile, "profile", "", "browser profile name, directory or cookie database path")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("auth %s does not accept positional arguments", args[0])
	}
	path, err := authFile()
	if err != nil {
		return err
	}
	if args[0] == "status" {
		session, err := loadSession(path)
		if err != nil {
			return err
		}
		client, err := sessionClient(session)
		if err != nil {
			return err
		}
		if err := client.checkAuth(ctx); err != nil {
			return fmt.Errorf("authentication check: %w", err)
		}
		fmt.Fprintf(out, "Authenticated (verified with portal).\nBrowser: %s\nImported: %s\nSession file: %s\n", session.Browser, session.ImportedAt.Format(time.RFC3339), path)
		if session.Profile != "" {
			fmt.Fprintf(out, "Profile: %s\n", session.Profile)
		}
		return nil
	}
	selected, err := domain.NewBrowserFromValue(browser)
	if err != nil {
		return err
	}
	return importSession(ctx, selected, profile, path, out)
}

func importSession(ctx context.Context, browser domain.Browser, profile, path string, out io.Writer) error {
	source := sweetcookie.Browser(browser.String())
	options := sweetcookie.Options{
		URL: portalURL + "/", Origins: []string{portalURL + "/gas/", portalURL + "/personal-account/", portalURL + "/personal_account/"},
		Browsers: []sweetcookie.Browser{source}, Mode: sweetcookie.ModeFirst, Timeout: 15 * time.Second,
	}
	if profile != "" {
		options.Profiles = map[sweetcookie.Browser]string{source: profile}
	}
	result, err := sweetcookie.Get(ctx, options)
	if err != nil || len(result.Cookies) == 0 {
		return fmt.Errorf("SweetCookie could not import usable portal cookies from %s (%d warnings); log in there first, check OS keychain access, or select --profile", browser, len(result.Warnings))
	}
	session := savedSession{Version: 1, Browser: browser.String(), Profile: profile, ImportedAt: time.Now().UTC()}
	for _, cookie := range result.Cookies {
		value := &http.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path, Secure: cookie.Secure, HttpOnly: cookie.HTTPOnly}
		if cookie.Expires != nil {
			value.Expires = *cookie.Expires
		}
		session.Cookies = append(session.Cookies, value)
	}
	client, err := sessionClient(session)
	if err != nil {
		return err
	}
	if err := client.checkAuth(ctx); err != nil {
		return fmt.Errorf("imported cookies were not saved: %w", err)
	}
	if err := saveSession(path, session); err != nil {
		return err
	}
	fmt.Fprintf(out, "Authenticated: imported %d portal cookies from %s and verified the session.\nPrivate session file: %s\n", len(session.Cookies), browser, path)
	if len(result.Warnings) != 0 {
		fmt.Fprintf(out, "SweetCookie reported %d browser warnings; session verification succeeded.\n", len(result.Warnings))
	}
	return nil
}
