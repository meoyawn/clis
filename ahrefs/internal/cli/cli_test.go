package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthImportThenStatusUsesSavedJar(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.json")
	jar := filepath.Join(dir, "session.json")
	if err := os.WriteFile(source, []byte(`[{"name":"BSSESSID","value":"SYNTHETIC_SECRET","domain":".ahrefs.com","path":"/"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"auth", "--session-file", jar, "import", "--cookies-file", source}
	if err := Run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("import: %v; stderr: %s", err, &stderr)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(t.Context(), []string{"auth", "status", "--session-file", jar}, &stdout, &stderr); err != nil {
		t.Fatalf("status: %v; stderr: %s", err, &stderr)
	}
	var status struct {
		Path             string `json:"path"`
		Imported         bool   `json:"imported"`
		SessionAvailable bool   `json:"session_available"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Path != jar || !status.Imported || !status.SessionAvailable || strings.Contains(stdout.String(), "SYNTHETIC_SECRET") {
		t.Fatalf("unexpected status: %s", &stdout)
	}
}

func TestInvalidViewsFailBeforeReadingSession(t *testing.T) {
	for _, test := range []struct {
		args    []string
		message string
	}{
		{[]string{"issues", "https://app.ahrefs.com/site-audit/123/issues?segment=unsupported"}, "unsupported URL parameter"},
		{[]string{"issues", "https://app.ahrefs.com/site-audit/123/data-explorer?filterId=synthetic"}, "use pages"},
		{[]string{"pages", "123", "--limit", "0"}, "invalid limit"},
		{[]string{"pages", "123", "--compare-with", "latest"}, "compare-with"},
		{[]string{"auth", "status", "--browser-profile", "Another profile"}, "apply to auth import"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, test.args...), "--session-file", filepath.Join(t.TempDir(), "missing.json"))
			err := Run(t.Context(), args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), test.message) || stdout.Len() != 0 {
				t.Fatalf("result = %q; error = %v; want %q", &stdout, err, test.message)
			}
		})
	}
}
