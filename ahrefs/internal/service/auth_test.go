package service

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportPersistsScopedJarAndWorkflowDoesNotReadSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.json")
	jar := filepath.Join(dir, "config", "session.json")
	input := `[
		{"name":"BSSESSID","value":"SYNTHETIC_SESSION","domain":".ahrefs.com","path":"/","secure":true},
		{"name":"cf_clearance","value":"SYNTHETIC_CLEARANCE","domain":".ahrefs.com","path":"/","secure":true},
		{"name":"BSSESSID","value":"SYNTHETIC_FOREIGN","domain":"other.test","path":"/"},
		{"name":"tracking","value":"SYNTHETIC_TRACKING","domain":".ahrefs.com","path":"/"}
	]`
	if err := os.WriteFile(source, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := ImportSession(t.Context(), jar, "Synthetic profile", source)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Imported || !status.SessionAvailable || len(status.CookieNames) != 2 {
		t.Fatalf("import status = %+v", status)
	}
	info, err := os.Stat(jar)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("session permissions = %v, %v", info, err)
	}
	saved, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "SYNTHETIC_FOREIGN") || strings.Contains(string(saved), "SYNTHETIC_TRACKING") {
		t.Fatalf("unrelated cookies persisted")
	}
	// Subsequent operations must need neither the source file nor a browser.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	status, err = SessionStatus(jar)
	if err != nil || status.Profile != "Synthetic profile" || !status.SessionAvailable {
		t.Fatalf("saved status = %+v, %v", status, err)
	}
	encoded, err := json.Marshal(status)
	if err != nil || strings.Contains(string(encoded), "SYNTHETIC_SESSION") || strings.Contains(string(encoded), "SYNTHETIC_CLEARANCE") {
		t.Fatalf("status exposed credentials: %s, %v", encoded, err)
	}
	httpClient, err := SessionClient(jar)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	routeTo(t, httpClient, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Cookie") != "BSSESSID=SYNTHETIC_SESSION; cf_clearance=SYNTHETIC_CLEARANCE" {
			t.Errorf("unexpected cookie scope")
		}
		w.Header().Set("Location", "https://other.test/steal-session")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := NewClient(httpClient).Projects(t.Context()); err == nil || !strings.Contains(err.Error(), "302") || requests != 1 {
		t.Fatalf("redirect result = %v; requests = %d", err, requests)
	}
}

func TestSessionStatusDistinguishesMissingExpiredAndUnsafeJar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	status, err := SessionStatus(path)
	if err != nil || status.Imported || status.SessionAvailable {
		t.Fatalf("missing jar = %+v, %v", status, err)
	}
	expiry := time.Now().Add(-time.Hour)
	s := session{Profile: "Synthetic", ImportedAt: time.Now().Add(-2 * time.Hour), Cookies: []savedCookie{
		{Name: "BSSESSID", Value: "SYNTHETIC_EXPIRED", Domain: ".ahrefs.com", Path: "/", Expires: &expiry},
	}}
	if err := saveSession(path, s); err != nil {
		t.Fatal(err)
	}
	status, err = SessionStatus(path)
	if err != nil || !status.Imported || status.SessionAvailable || len(status.CookieNames) != 0 {
		t.Fatalf("expired jar = %+v, %v", status, err)
	}
	if _, err := SessionClient(path); err == nil || !strings.Contains(err.Error(), "auth import") {
		t.Fatalf("expired session accepted: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := SessionStatus(path); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("unsafe jar accepted: %v", err)
	}
}
