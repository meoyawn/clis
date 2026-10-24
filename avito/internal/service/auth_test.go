package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sweetcookie"
)

func syntheticCookieReader(cookies ...sweetcookie.Cookie) CookieReader {
	return func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
		return sweetcookie.Result{Cookies: cookies}, nil
	}
}

func testAuthStore(t *testing.T) AuthStore {
	t.Helper()
	store := AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	if _, err := store.Import(t.Context(), ChromeBrowser(), "", syntheticCookieReader(sweetcookie.Cookie{
		Name: "session", Value: "SYNTHETIC_AUTH", Domain: ".avito.ru", Path: "/",
	})); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestAuthImportScopesCookiesAndProtectsStore(t *testing.T) {
	t.Parallel()

	store := AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	expired, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	reads := 0
	status, err := store.Import(t.Context(), ChromeBrowser(), "Profile 1", func(_ context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		reads++
		if opts.URL != WebOrigin+"/" || len(opts.Origins) != 4 || opts.Origins[0] != WebOrigin+"/web/1/js/items" || opts.Origins[1] != WebOrigin+authProfilesPath || opts.Origins[2] != WebOrigin+locationsPath || opts.Origins[3] != WebOrigin+filterSuggestPath || len(opts.Browsers) != 1 || opts.Browsers[0] != sweetcookie.BrowserChrome || opts.Profiles[sweetcookie.BrowserChrome] != "Profile 1" || opts.AllowAllHosts {
			t.Fatalf("wrong import options: %+v", opts)
		}
		return sweetcookie.Result{Cookies: []sweetcookie.Cookie{
			{Name: "session", Value: "SYNTHETIC_ROOT", Domain: ".avito.ru", Path: ""},
			{Name: "session", Value: "SYNTHETIC_API", Domain: "www.avito.ru", Path: "/web/1", Expires: &future},
			{Name: "foreign", Value: "DO_NOT_SAVE", Domain: "other.test", Path: "/"},
			{Name: "app", Value: "DO_NOT_SAVE", Domain: "app.avito.ru", Path: "/"},
			{Name: "other_path", Value: "DO_NOT_SAVE", Domain: "avito.ru", Path: "/messenger"},
			{Name: "wrong_boundary", Value: "DO_NOT_SAVE", Domain: "avito.ru", Path: "/web/1/js/item"},
			{Name: "expired", Value: "DO_NOT_SAVE", Domain: "avito.ru", Path: "/", Expires: &expired},
			{Name: "bad;name", Value: "DO_NOT_SAVE", Domain: "avito.ru", Path: "/"},
		}, Warnings: []string{"SYNTHETIC_PRIVATE_WARNING"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || status.CookieCount != 2 || status.UsableCookieCount != 2 || status.NextExpiry == nil || !status.NextExpiry.Equal(future) || status.LoginVerified || status.HTTPRequests != 0 {
		t.Fatalf("incorrect import summary: %+v", status)
	}
	output, marshalErr := json.Marshal(status)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(output), "SYNTHETIC_") || strings.Contains(string(output), "DO_NOT_SAVE") {
		t.Fatal("import output leaked credentials or helper diagnostics")
	}
	for path, mode := range map[string]os.FileMode{store.Path: 0600, filepath.Dir(store.Path): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("incorrect permissions for %s: %v", path, err)
		}
	}
	data, err := os.ReadFile(store.Path)
	if err != nil || bytes.Contains(data, []byte("DO_NOT_SAVE")) {
		t.Fatal("stored an out-of-scope cookie")
	}
	client, clientErr := NewClient(NewHTTPClient(), store)
	if clientErr != nil {
		t.Fatal(clientErr)
	}
	u, parseErr := url.Parse(WebOrigin + "/web/1/js/items?name=tv")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	request, err := client.Prepare(t.Context(), u, RequestOptions{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if request.request.Header.Get("Cookie") != "session=SYNTHETIC_API; session=SYNTHETIC_ROOT" {
		t.Fatal("cookie path precedence was lost")
	}
	u, parseErr = url.Parse(WebOrigin + "/kazan/audio_i_video/tv_1000000002")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	request, err = client.Prepare(t.Context(), u, RequestOptions{}, true)
	if err != nil || request.request.Header.Get("Cookie") != "session=SYNTHETIC_ROOT" {
		t.Fatalf("API cookie escaped its path: %v", err)
	}
	if err := os.WriteFile(store.Path, []byte("INVALID_SYNTHETIC_AUTH"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Prepare(t.Context(), u, RequestOptions{}, true); err == nil || strings.Contains(err.Error(), "SYNTHETIC_AUTH") {
		t.Fatalf("unsafe corrupt-store request: %v", err)
	}
}

func TestFailedAuthImportsPreservePreviousStore(t *testing.T) {
	t.Parallel()

	store := AuthStore{Path: filepath.Join(t.TempDir(), "avito", "auth.json")}
	reader := syntheticCookieReader(sweetcookie.Cookie{Name: "session", Value: "SYNTHETIC_ORIGINAL", Domain: ".avito.ru", Path: "/"})
	if _, err := store.Import(t.Context(), ChromeBrowser(), "", reader); err != nil {
		t.Fatal(err)
	}
	before, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, read := range []CookieReader{
		func(context.Context, sweetcookie.Options) (sweetcookie.Result, error) {
			return sweetcookie.Result{}, errors.New("SYNTHETIC_SECRET")
		},
		syntheticCookieReader(),
		syntheticCookieReader(sweetcookie.Cookie{Name: "session", Value: "SYNTHETIC_FOREIGN", Domain: "other.test", Path: "/"}),
	} {
		_, err := store.Import(t.Context(), ChromeBrowser(), "", read)
		if err == nil || strings.Contains(err.Error(), "SYNTHETIC_") {
			t.Fatalf("unsafe import failure: %v", err)
		}
		after, readErr := os.ReadFile(store.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed import replaced prior credentials")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, err := store.Import(ctx, ChromeBrowser(), "", func(ctx context.Context, opts sweetcookie.Options) (sweetcookie.Result, error) {
		cancel()
		return reader(ctx, opts)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("canceled import replaced prior credentials")
	}
}

func TestAuthStatusMissingExpiredCorruptAndUnsafeStores(t *testing.T) {
	t.Parallel()

	store := AuthStore{Path: filepath.Join(t.TempDir(), "auth.json")}
	status, err := store.Status()
	if err != nil || status.Imported || status.CookieCount != 0 || status.HTTPRequests != 0 {
		t.Fatalf("missing-store status: %+v, %v", status, err)
	}
	if _, err := os.Stat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status wrote a store")
	}
	if _, err := store.Import(t.Context(), ChromeBrowser(), "", syntheticCookieReader(sweetcookie.Cookie{Name: "session", Value: "SYNTHETIC_SECRET", Domain: "avito.ru", Path: "/"})); err != nil {
		t.Fatal(err)
	}
	record, loadErr := store.load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	expired := time.Now().Add(-time.Hour)
	record.Cookies[0].Expires = &expired
	data, marshalErr := json.Marshal(record)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if err := writeAtomic(store.Path, data); err != nil {
		t.Fatal(err)
	}
	status, err = store.Status()
	if err != nil || !status.Imported || status.UsableCookieCount != 0 || status.ExpiredCookieCount != 1 || status.NextExpiry != nil {
		t.Fatalf("expired-store status: %+v, %v", status, err)
	}
	u, parseErr := url.Parse(WebOrigin + "/web/1/js/items")
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if _, err := store.cookiesFor(u); err == nil || !strings.Contains(err.Error(), "no unexpired") {
		t.Fatalf("expired cookies were usable: %v", err)
	}
	for _, bad := range [][]byte{[]byte("SYNTHETIC_SECRET"), bytes.Replace(data, []byte("avito.ru"), []byte("other.test"), 1)} {
		if err := writeAtomic(store.Path, bad); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Status(); err == nil || strings.Contains(err.Error(), "SYNTHETIC_SECRET") {
			t.Fatalf("unsafe corrupt-store status: %v", err)
		}
	}
	if err := writeAtomic(store.Path, data); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Path, 0644); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	if _, err := store.Status(); err == nil || !strings.Contains(err.Error(), "only by its owner") {
		t.Fatalf("accepted readable-by-others credentials: %v", err)
	}
	if err := os.Remove(store.Path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(store.Path), "linked.json")
	if err := writeAtomic(target, data); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(); err == nil {
		t.Fatal("accepted a symlink auth store")
	}
}
