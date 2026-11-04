package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSweetCookieImportScopesPersistsAndStatusIsLocal(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "source.json"), filepath.Join(dir, "saved", "session.json")
	input := `[
		{"name":"_career_session","value":"SYNTHETIC_SESSION","domain":"career.habr.com","path":"/","secure":true},
		{"name":"qrator_msid2","value":"SYNTHETIC_PROTECTION","domain":".habr.com","path":"/"},
		{"name":"_career_session","value":"SYNTHETIC_FOREIGN","domain":"other.test","path":"/"},
		{"name":"analytics","value":"SYNTHETIC_TRACKING","domain":"career.habr.com","path":"/"}
	]`
	if err := os.WriteFile(source, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := ImportSession(t.Context(), destination, "Synthetic", source)
	if err != nil || !status.SessionAvailable || len(status.CookieNames) != 2 {
		t.Fatalf("import = %+v %v", status, err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	status, err = SessionStatus(destination)
	if err != nil || status.Profile != "Synthetic" || !status.SessionAvailable {
		t.Fatalf("status from saved jar = %+v %v", status, err)
	}
	b, _ := json.Marshal(status)
	if strings.Contains(string(b), "SYNTHETIC_SESSION") || strings.Contains(string(b), "SYNTHETIC_PROTECTION") {
		t.Fatal("status exposed cookie values")
	}
	saved, err := os.ReadFile(destination)
	if err != nil || strings.Contains(string(saved), "SYNTHETIC_FOREIGN") || strings.Contains(string(saved), "SYNTHETIC_TRACKING") {
		t.Fatalf("unrelated cookies persisted: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe jar permissions: %v %v", info, err)
	}
	httpClient, err := SessionClient(destination)
	if err != nil {
		t.Fatal(err)
	}
	httpClient.CloseIdleConnections()
}

func TestSessionStatusMissingExpiredAndUnsafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	status, err := SessionStatus(path)
	if err != nil || status.Imported || status.SessionAvailable {
		t.Fatalf("missing session = %+v %v", status, err)
	}
	expired := time.Now().Add(-time.Hour)
	if err := SavePrivateJSON(path, session{Profile: "Synthetic", ImportedAt: time.Now().Add(-2 * time.Hour), Cookies: []savedCookie{{Name: "_career_session", Value: "SYNTHETIC_EXPIRED", Domain: "career.habr.com", Path: "/", Expires: &expired}}}); err != nil {
		t.Fatal(err)
	}
	status, err = SessionStatus(path)
	if err != nil || !status.Imported || status.SessionAvailable {
		t.Fatalf("expired session = %+v %v", status, err)
	}
	if _, err := SessionClient(path); err == nil {
		t.Fatal("expired cookie created a workflow client")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := SessionStatus(path); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("unsafe jar accepted: %v", err)
	}
}
