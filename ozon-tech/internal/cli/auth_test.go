package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthImportScopesSecretsAndStatusUsesSavedCookies(t *testing.T) {
	dir := t.TempDir()
	source, jar := filepath.Join(dir, "cookies.json"), filepath.Join(dir, "config", "session.json")
	input := `[
		{"name":"__Secure-ETC","value":"SYNTHETIC_CLEARANCE","domain":".ozon.tech","path":"/","secure":true},
		{"name":"abt_data","value":"SYNTHETIC_ANTIBOT","domain":"ozon.tech","path":"/","secure":true},
		{"name":"__Secure-refresh-token","value":"SYNTHETIC_ACCOUNT_SECRET","domain":"ozon.tech","path":"/","secure":true},
		{"name":"__Secure-ETC","value":"SYNTHETIC_FOREIGN_SECRET","domain":"ozon.ru","path":"/","secure":true}
	]`
	if err := os.WriteFile(source, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(t.Context(), []string{"auth", "import", "--session-file", jar, "--cookies-file", source}, &stdout); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "SYNTHETIC_") {
		t.Fatal("auth import exposed cookie values")
	}
	saved, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "SYNTHETIC_CLEARANCE") || strings.Contains(string(saved), "SYNTHETIC_ACCOUNT_SECRET") || strings.Contains(string(saved), "SYNTHETIC_FOREIGN_SECRET") {
		t.Fatal("import did not isolate Ozon Tech antibot cookies")
	}
	info, err := os.Stat(jar)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private jar permissions: %v %v", info, err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(t.Context(), []string{"auth", "status", "--session-file", jar}, &stdout); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Imported         bool     `json:"imported"`
		CookiesAvailable bool     `json:"cookies_available"`
		CookieNames      []string `json:"cookie_names"`
		APIAvailable     *bool    `json:"api_available"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Imported || !status.CookiesAvailable || len(status.CookieNames) != 2 || status.APIAvailable != nil || strings.Contains(stdout.String(), "SYNTHETIC_") {
		t.Fatalf("saved status = %s", &stdout)
	}
	if err := os.Chmod(jar, 0644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(t.Context(), []string{"auth", "status", "--session-file", jar}, &stdout); err == nil || stdout.Len() != 0 {
		t.Fatal("unsafe jar was accepted or produced successful JSON")
	}
}
