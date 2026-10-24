package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Captured in Chrome's profile switcher; this read-only request returns the
// current profile without fetching a listing or changing the selected account.
const authProfilesPath = "/web/1/profiles/list"

type Account struct {
	ID   int64  `json:"id"`
	Name string `json:"name,omitempty"`
}

func (c *Client) ImportAuth(ctx context.Context, browser Browser, profile string, read CookieReader, options RequestOptions) (*AuthStatus, error) {
	record, status, err := c.auth.prepareImport(ctx, browser, profile, read)
	if err != nil {
		status = &AuthStatus{Store: c.auth.Path, Browser: browser.String(), Profile: profile, CookieNames: []string{}}
		return status.failure(err.Error(), err)
	}
	if _, err := c.checkAuth(ctx, record, status, options); err != nil {
		return status, err
	}
	if err := c.auth.save(ctx, record); err != nil {
		return status.failure("Your Avito sign-in was verified, but the session could not be saved. "+err.Error(), err)
	}
	status.Imported = true
	return status, nil
}

func (c *Client) AuthStatus(ctx context.Context, options RequestOptions) (*AuthStatus, error) {
	record, err := c.auth.load()
	if err != nil {
		status := &AuthStatus{Store: c.auth.Path, CookieNames: []string{}}
		if errors.Is(err, os.ErrNotExist) {
			status.IsAuthenticated = boolPointer(false)
			return status.failure("Not signed in. "+status.signInHint(), nil)
		}
		return status.failure("Could not read your saved Avito session. "+status.signInHint(), err)
	}
	status := record.summary(c.auth.Path, time.Now())
	if status.UsableCookieCount == 0 {
		status.IsAuthenticated = boolPointer(false)
		return status.failure("Your saved Avito session has expired. "+status.signInHint(), nil)
	}
	return c.checkAuth(ctx, record, status, options)
}

func (c *Client) checkAuth(ctx context.Context, record *authRecord, status *AuthStatus, options RequestOptions) (*AuthStatus, error) {
	target := &url.URL{Scheme: "https", Host: "www.avito.ru", Path: authProfilesPath, RawQuery: "limit=3&withEntities=true"}
	request, err := c.prepare(ctx, target, options, false, record.cookiesFor)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return status.checkFailure(err)
		}
		return status.failure("Could not check this Avito session. "+status.signInHint(), err)
	}
	request.request.Header.Set("Cache-Control", "no-cache, no-store, max-age=0")
	status.HTTPRequests = 1
	checked := time.Now().UTC()
	status.CheckedAt = &checked
	response, err := c.Do(request)
	if err != nil {
		return status.checkFailure(err)
	}
	account, err := parseAuthProfile(response.Body)
	if err != nil {
		return status.failure("Could not verify your Avito sign-in: Avito returned an unexpected response. Try avito auth status again later.", err)
	}
	status.LoginVerified = true
	status.IsAuthenticated = boolPointer(account != nil)
	status.Account = account
	status.Warnings = nil
	if account == nil {
		return status.failure("Not signed in. "+status.signInHint(), nil)
	}
	return status, nil
}

// Only the explicitly current profile is an identity. Do not guess from the
// first linked profile, cookie names, or a successful HTTP status alone.
func parseAuthProfile(body []byte) (*Account, error) {
	var payload struct {
		Status string `json:"status"`
		Result *struct {
			Count    *int `json:"count"`
			Profiles []struct {
				ID        int64  `json:"profileId"`
				Name      string `json:"name"`
				IsCurrent bool   `json:"isCurrent"`
			} `json:"profiles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Avito profile response: %w", err)
	}
	result := payload.Result
	if payload.Status != "ok" || result == nil || result.Count == nil || *result.Count < 0 || result.Profiles == nil || *result.Count < len(result.Profiles) {
		return nil, fmt.Errorf("profile response from Avito has no valid result.profiles/count")
	}
	if *result.Count == 0 {
		return nil, nil
	}
	var account *Account
	for _, profile := range result.Profiles {
		if !profile.IsCurrent {
			continue
		}
		if profile.ID <= 0 || account != nil {
			return nil, fmt.Errorf("profile response from Avito has an invalid or ambiguous current profile")
		}
		account = &Account{ID: profile.ID, Name: strings.TrimSpace(profile.Name)}
	}
	if account == nil {
		return nil, fmt.Errorf("profile response from Avito does not identify the current profile")
	}
	return account, nil
}

func (status *AuthStatus) checkFailure(err error) (*AuthStatus, error) {
	var failure *HTTPError
	switch {
	case errors.Is(err, context.Canceled):
		return status.failure("Avito sign-in check canceled.", err)
	case errors.Is(err, context.DeadlineExceeded):
		return status.failure("Avito sign-in check timed out. Try avito auth status again.", err)
	case errors.As(err, &failure):
		status.RetryAfter = failure.RetryAfter
		switch failure.Status {
		case http.StatusUnauthorized:
			status.LoginVerified = true
			status.IsAuthenticated = boolPointer(false)
			status.Warnings = nil
			return status.failure("Not signed in. "+status.signInHint(), err)
		case http.StatusForbidden, http.StatusOK:
			return status.failure("Avito requires a browser check. Open avito.ru in your browser, complete any check, then run:\n  "+status.ImportCommand(), err)
		case http.StatusTooManyRequests:
			message := "Avito is temporarily limiting requests. Try again later."
			if failure.RetryAfter != "" {
				message += " Retry-After: " + failure.RetryAfter
			}
			return status.failure(message, err)
		default:
			return status.failure("Avito could not check your sign-in right now. Try avito auth status again later.", err)
		}
	default:
		return status.failure("Could not reach Avito to check your sign-in. Check your connection and try avito auth status again.", err)
	}
}

func (status *AuthStatus) signInHint() string {
	return "Open avito.ru in your browser, sign in, then run:\n  " + status.ImportCommand()
}

func (status *AuthStatus) ImportCommand() string {
	browser, err := NewBrowserFromValue(status.Browser)
	if err != nil {
		browser = ChromeBrowser()
	}
	command := "avito auth import --browser " + browser.String()
	if status.Profile != "" {
		command += " --browser-profile '" + strings.ReplaceAll(status.Profile, "'", "'\\''") + "'"
	}
	return command
}

// Keep raw browser/server diagnostics out of displayed errors while preserving
// errors.Is/As for callers (including HTTP status and Retry-After).
type authCheckError struct {
	message string
	cause   error
}

func (err *authCheckError) Error() string { return err.message }
func (err *authCheckError) Unwrap() error { return err.cause }

func (status *AuthStatus) failure(message string, cause error) (*AuthStatus, error) {
	status.Error = message
	return status, &authCheckError{message: message, cause: cause}
}

func boolPointer(value bool) *bool { return &value }
