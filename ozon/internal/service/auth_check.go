package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) //nolint:errcheck // Best-effort cleanup; after a successful rename the temporary path is absent.
	if _, err = f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (c *Client) checkAuth(ctx context.Context, r *authRecord, s *AuthStatus, opt RequestOptions) error {
	s.Warnings = nil
	p, err := c.fetchAuth(ctx, r, opt)
	if p != nil {
		s.HTTPRequests = p.meta.HTTPRequests
	}
	now := time.Now().UTC()
	s.CheckedAt = &now
	if err == nil {
		u := p.UserInfo.User
		s.IsAuthenticated, err = p.loginState()
		s.LoginVerified = s.IsAuthenticated != nil
		switch {
		case err != nil:
		case s.IsAuthenticated == nil:
			err = fmt.Errorf("Ozon did not report login status; session could not be verified")
		case !*s.IsAuthenticated:
			err = fmt.Errorf("Ozon returned a signed-out page for the selected browser cookies; refresh ozon.ru in that browser profile, then run ozon auth import again")
		default:
			s.Account = &Account{ID: u.ID.String(), Name: strings.TrimSpace(u.FirstName + " " + u.LastName)}
		}
	}
	if err != nil {
		s.Error = err.Error()
		var h *HTTPError
		if errors.As(err, &h) {
			s.RetryAfter = h.RetryAfter
		}
	}
	return err
}

func (p *page) loginState() (*bool, error) {
	loggedIn := p.UserInfo.User.LoggedIn
	// Current anonymous account pages omit userInfo entirely and identify
	// their signed-out state through this dedicated account-menu widget.
	anonymous, err := p.first("profileMenuAnonymous")
	if err != nil {
		return nil, fmt.Errorf("Ozon returned an invalid account menu; session could not be verified")
	}
	if anonymous != nil {
		if loggedIn != nil && *loggedIn {
			return nil, fmt.Errorf("Ozon returned conflicting login states; session could not be verified")
		}
		return boolean(false), nil
	}
	return loggedIn, nil
}

const maxAuthRedirects = 3

func authPageURL(raw string) (*url.URL, error) {
	clean, err := safePageURL(raw)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(clean)
	if err != nil {
		return nil, err
	}
	if u.Path != "/my/main" && u.Path != "/my/main/" {
		return nil, fmt.Errorf("auth redirect must stay on the Ozon account page")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("invalid auth redirect parameters")
	}
	for key, values := range q {
		if (key != "__rr" && key != "page_changed") || len(values) != 1 || values[0] == "" {
			return nil, fmt.Errorf("unsupported auth redirect parameters")
		}
	}
	u.RawQuery = q.Encode()
	return u, nil
}

func (c *Client) fetchAuth(ctx context.Context, record *authRecord, opt RequestOptions) (*page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := "/my/main"
	target := entrypointURL(path)
	cookies, err := record.cookiesFor(target)
	if err != nil {
		return nil, err
	}
	// Redirect challenge cookies live only in this invocation. Authentication
	// credentials stay identical to the imported browser snapshot.
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	jar.SetCookies(target, cookies)
	cookiesFor := func(u *url.URL) ([]*http.Cookie, error) { return jar.Cookies(u), nil }
	visited := map[string]bool{target.String(): true}
	var result *page
	requests := 0
	for redirects := 0; ; redirects++ {
		p, err := c.fetchPage(ctx, path, target, opt, cookiesFor)
		if p != nil {
			requests += p.meta.HTTPRequests
			p.meta.HTTPRequests = requests
			result = p
		}
		var redirect *pageRedirectError
		if !errors.As(err, &redirect) {
			return result, err
		}
		if redirects == maxAuthRedirects {
			return result, fmt.Errorf("%w: exceeded %d auth redirects", err, maxAuthRedirects)
		}
		next, nextTarget, validationErr := pageRedirectURL(redirect.target, authPageURL)
		if validationErr != nil {
			// Do not expose a rejected redirect's query values in diagnostics.
			return result, fmt.Errorf("%w: invalid auth redirect", err)
		}
		if visited[nextTarget.String()] {
			return result, fmt.Errorf("%w: auth redirect loop", err)
		}
		visited[nextTarget.String()] = true
		jar.SetCookies(target, redirect.cookies)
		path, target = next.String(), nextTarget
	}
}

func (c *Client) AuthStatus(ctx context.Context, opt RequestOptions) (*AuthStatus, error) {
	s, err := c.auth.Status()
	if err != nil {
		return nil, err
	}
	if !s.Imported {
		err = fmt.Errorf("no saved Ozon session; run ozon auth import --browser chrome")
		s.Error = err.Error()
		return s, err
	}
	saved, err := c.auth.load()
	if err != nil {
		return s, err
	}
	// Status uses the same saved credentials as product requests. It neither
	// reads the browser nor persists request-local session cookies.
	err = c.checkAuth(ctx, saved, s, opt)
	return s, err
}

// ImportAuth copies the browser session without contacting Ozon. In particular,
// importing must not consume or rotate the browser's renewal credentials.
func (c *Client) ImportAuth(ctx context.Context, b Browser, profile string, read CookieReader) (*AuthStatus, error) {
	r, s, err := c.auth.prepareImport(ctx, b, profile, read)
	if err != nil {
		return nil, err
	}
	if err := c.auth.save(ctx, r); err != nil {
		s.Error = err.Error()
		return s, err
	}
	s.Imported = true
	return s, nil
}
