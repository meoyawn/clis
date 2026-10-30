package service

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steipete/sweetcookie"
)

func TestBrowserImportAndPrivateStorage(t *testing.T) {
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	result := sweetcookie.Result{Cookies: []sweetcookie.Cookie{
		{Name: "__ddg2_", Value: "private-clearance", Domain: ".megastroy.com", Path: "/", Source: sweetcookie.Source{Profile: "Default", StorePath: "profile/Cookies"}},
		{Name: "__ddg8_", Value: "expired", Domain: ".megastroy.com", Path: "/", Expires: &expired},
		{Name: "PHPSESSID", Value: "account-session", Domain: ".megastroy.com", Path: "/"},
		{Name: "__ddg2_", Value: "other-site", Domain: ".example.com", Path: "/"},
	}}
	session, err := sessionFromBrowser(result, Chrome(), "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	store := AuthStore{Path: filepath.Join(t.TempDir(), "megastroy", "auth.json")}
	if err := store.Save(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Cookies) != 1 || loaded.Cookies[0].Value != "private-clearance" || loaded.Profile != "Default" {
		t.Fatalf("import included unrelated or expired browser data: %+v", loaded)
	}
	for _, path := range []string{store.Path, filepath.Dir(store.Path)} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("auth storage must be owner-only: %s %v", path, err)
		}
	}
	status, err := store.Status()
	if err != nil {
		t.Fatal(err)
	}
	statusJSON, err := json.Marshal(status)
	if err != nil || strings.Contains(string(statusJSON), "private-clearance") || strings.Contains(string(statusJSON), "account-session") || !status.Imported || status.UsableCookieCount != 1 {
		t.Fatalf("status disclosed secrets or misreported availability: %s %v", statusJSON, err)
	}
	// Failed replacements must leave the usable previous store intact.
	invalid := loaded
	invalid.Cookies = []Cookie{{Name: "PHPSESSID", Value: "account-session", Domain: ".megastroy.com", Path: "/"}}
	if err := store.Save(context.Background(), invalid); err == nil {
		t.Fatal("stored an account cookie")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Save(canceled, loaded); err == nil {
		t.Fatal("saved after cancellation")
	}
	loaded, err = store.Load()
	if err != nil || len(loaded.Cookies) != 1 || loaded.Cookies[0].Value != "private-clearance" {
		t.Fatalf("failed save lost previous clearance: %+v %v", loaded, err)
	}
	if err := os.WriteFile(store.Path, []byte("{broken json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("accepted a corrupt auth store")
	}
}

func TestImportRejectsMixedProfilesAndEmptyClearance(t *testing.T) {
	for _, cookies := range [][]sweetcookie.Cookie{
		{},
		{
			{Name: "__ddg2_", Value: "one", Domain: ".megastroy.com", Path: "/", Source: sweetcookie.Source{StorePath: "one/Cookies"}},
			{Name: "__ddg5_", Value: "two", Domain: ".megastroy.com", Path: "/", Source: sweetcookie.Source{StorePath: "two/Cookies"}},
		},
	} {
		if _, err := sessionFromBrowser(sweetcookie.Result{Cookies: cookies}, Chrome(), "", "", time.Now()); err == nil {
			t.Fatal("accepted missing clearance or mixed browser identities")
		}
	}
}

func TestCookieDeletionAndExpiration(t *testing.T) {
	now := time.Now()
	expired := now.Add(-time.Second)
	session := Session{Cookies: []Cookie{
		{Name: "__ddg2_", Value: "expired", Domain: ".megastroy.com", Path: "/", Expires: &expired},
		{Name: "__ddg8_", Value: "deleted", Domain: ".megastroy.com", Path: "/"},
		{Name: "__ddg5_", Value: "keep", Domain: ".megastroy.com", Path: "/"},
	}}
	session.Merge([]*http.Cookie{
		{Name: "__ddg8_", Domain: "megastroy.com", Path: "/", MaxAge: -1},
		{Name: "__ddg5_", Value: "wrong-domain", Domain: ".other.com", Path: "/"},
	}, now)
	cookies := session.HTTPCookies(now)
	if len(cookies) != 1 || cookies[0].Name != "__ddg5_" || cookies[0].Value != "keep" {
		t.Fatalf("sent deleted, expired, or foreign cookies: %+v", cookies)
	}
}
