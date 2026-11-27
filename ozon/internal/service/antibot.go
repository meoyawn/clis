package service

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// antibotCookie is set by Ozon after a browser passes its JavaScript check
// (/abt/result). Ozon rejects stale values with a 403 challenge page.
const antibotCookie = "abt_data"

// BrowserOpener shows a page in the browser the session was imported from.
type BrowserOpener func(ctx context.Context, browser Browser, target string) error

// IsBrowserChallenge reports whether Ozon rejected the saved anti-bot cookie.
func IsBrowserChallenge(err error) bool {
	var challenge *browserChallengeError
	return errors.As(err, &challenge)
}

// RenewAntibot lets the imported browser profile pass Ozon's check and saves
// its renewed cookies. The CLI never answers the challenge itself: it opens
// ozon.ru in that browser once, then rereads its cookie store every poll
// interval until abt_data differs from the rejected value. Browsers write
// cookies to disk lazily, so a fresh value can appear seconds after the page.
func (c *Client) RenewAntibot(ctx context.Context, read CookieReader, open BrowserOpener, poll time.Duration) error {
	saved, err := c.auth.load()
	if err != nil {
		return err
	}
	browser, err := NewBrowserFromValue(saved.Browser)
	if err != nil {
		return err
	}
	stale := saved.cookie(antibotCookie)
	opened := false
	for {
		if fresh := c.browserSession(ctx, browser, saved, read); fresh != nil && fresh.cookie(antibotCookie) != "" && fresh.cookie(antibotCookie) != stale {
			return c.auth.save(ctx, fresh)
		}
		if !opened {
			if err := open(ctx, browser, WebOrigin+"/"); err != nil {
				return fmt.Errorf("open ozon.ru in %s to renew its anti-bot cookie: %w", browser, err)
			}
			opened = true
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not renew Ozon's anti-bot cookie before the timeout; load ozon.ru there, complete any check, then rerun: %w", browser, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// browserSession rereads the browser profile the session came from. Reads
// that fail or come from another profile are ignored.
func (c *Client) browserSession(ctx context.Context, browser Browser, saved *authRecord, read CookieReader) *authRecord {
	for _, profile := range []string{saved.Profile, ""} {
		fresh, _, err := c.auth.prepareImport(ctx, browser, profile, read)
		if err != nil {
			continue
		}
		if saved.CookieSource != "" && fresh.CookieSource != saved.CookieSource {
			continue
		}
		fresh.Profile = saved.Profile
		return fresh
	}
	return nil
}

func (record authRecord) cookie(name string) string {
	for _, cookie := range record.Cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
