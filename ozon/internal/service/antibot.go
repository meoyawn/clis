package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// AntibotCookie is set after a browser engine runs Ozon's anti-bot script.
// Ozon answers requests carrying a stale value with a 403 challenge page.
const AntibotCookie = "abt_data"

// antibotCookies are copied from the renewing engine; account cookies stay
// as imported from the user's browser.
var antibotCookies = [...]string{AntibotCookie, "__Secure-ETC"}

// AntibotSource loads ozon.ru in a real browser engine, lets Ozon's current
// script run there, and returns the cookies that engine then holds.
type AntibotSource func(ctx context.Context) ([]*http.Cookie, error)

// IsBrowserChallenge reports whether Ozon rejected the saved anti-bot cookie.
func IsBrowserChallenge(err error) bool {
	var challenge *browserChallengeError
	return errors.As(err, &challenge)
}

// SetAntibotSource enables renewing a rejected abt_data through source.
func (c *Client) SetAntibotSource(source AntibotSource) { c.antibot = source }

// renewAntibot obtains fresh anti-bot cookies once per client, saves them
// and makes every later request carry them.
func (c *Client) renewAntibot(ctx context.Context) error {
	c.antibotMu.Lock()
	defer c.antibotMu.Unlock()
	if c.antibot == nil {
		return errors.New("anti-bot renewal is not available")
	}
	if c.renewed {
		return errors.New("the renewed anti-bot cookie was rejected too")
	}
	c.renewed = true
	saved, err := c.auth.load()
	if err != nil {
		return err
	}
	fresh, err := c.antibot(ctx)
	if err != nil {
		return fmt.Errorf("renew Ozon's anti-bot cookie: %w", err)
	}
	var picked []*http.Cookie
	stale := map[string]string{}
	for _, name := range antibotCookies {
		stale[name] = saved.cookie(name)
		for _, cookie := range fresh {
			if cookie.Name != name {
				continue
			}
			stored := storedCookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly}
			if stored.Path == "" {
				stored.Path = "/"
			}
			if !cookie.Expires.IsZero() {
				expires := cookie.Expires.UTC()
				stored.Expires = &expires
			}
			if !stored.valid() || stored.expired(time.Now()) {
				continue
			}
			saved.replace(stored)
			picked = append(picked, &http.Cookie{Name: stored.Name, Value: stored.Value})
		}
	}
	if !slices.ContainsFunc(picked, func(c *http.Cookie) bool { return c.Name == AntibotCookie }) {
		return fmt.Errorf("renew Ozon's anti-bot cookie: the browser engine received no %s", AntibotCookie)
	}
	if err := c.auth.save(ctx, saved); err != nil {
		return err
	}
	c.fresh, c.stale = picked, stale
	return nil
}

// withAntibot swaps the rejected saved values for the renewed ones. Values
// Ozon itself set later in this command, such as during redirects, are newer
// than the renewal and stay as they are.
func (c *Client) withAntibot(cookies []*http.Cookie) []*http.Cookie {
	c.antibotMu.Lock()
	defer c.antibotMu.Unlock()
	if len(c.fresh) == 0 {
		return cookies
	}
	out := slices.Clone(cookies)
	for _, f := range c.fresh {
		i := slices.IndexFunc(out, func(old *http.Cookie) bool { return old.Name == f.Name })
		switch {
		case i < 0:
			out = append(out, f)
		case out[i].Value == c.stale[f.Name]:
			out[i] = f
		}
	}
	return out
}

func (record authRecord) cookie(name string) string {
	for _, cookie := range record.Cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (record *authRecord) replace(cookie storedCookie) {
	for i, old := range record.Cookies {
		if old.Name == cookie.Name {
			record.Cookies[i] = cookie
			return
		}
	}
	record.Cookies = append(record.Cookies, cookie)
}
