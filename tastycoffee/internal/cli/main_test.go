package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meoyawn/clis/tastycoffee/internal/service"
)

func TestAuthStatusAndSessionStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account", "session.json")
	s := service.Session{Profile: "Synthetic profile", ImportedAt: time.Now().UTC(), Cookie: service.SavedCookie{Name: "auth.token", Value: "SYNTHETIC_SECRET", Domain: "shop.tastycoffee.ru", Path: "/"}}
	if err := service.SaveSession(path, s); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session mode = %v", info.Mode())
	}
	var stdout bytes.Buffer
	if err := Run(t.Context(), []string{"auth", "--session-file", path, "status"}, &stdout); err != nil {
		t.Fatal(err)
	}
	var status service.AuthStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Imported || !status.SessionAvailable || status.Profile != s.Profile || strings.Contains(stdout.String(), "SYNTHETIC_SECRET") {
		t.Fatalf("wrong or unsafe auth status: %s", &stdout)
	}
	loaded, err := service.LoadSession(path)
	if err != nil || loaded.Cookie.Value != s.Cookie.Value {
		t.Fatalf("session did not round-trip: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	err = Run(t.Context(), []string{"auth", "status", "--session-file", path}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "owner-only") || stdout.Len() != 0 {
		t.Fatalf("unsafe session accepted: %v; stdout=%s", err, &stdout)
	}
}
