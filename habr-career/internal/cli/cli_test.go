package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meoyawn/clis/habr-career/internal/cli"
)

func TestOfflineCommandsAndInvalidArgumentsNeedNoSession(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-session.json")
	for _, args := range [][]string{
		{"--help"},
		{"application", "prepare", "--help"},
		{"auth", "status", "--session-file", missing},
	} {
		var stdout bytes.Buffer
		if err := cli.Run(t.Context(), args, &stdout, &bytes.Buffer{}); err != nil || stdout.Len() == 0 {
			t.Fatalf("offline %v: %v %s", args, err, stdout.String())
		}
	}
	for _, args := range [][]string{
		{"auth", "status", "--cookies-file", "must-not-open"},
		{"vacancy", "get", "https://foreign.test/vacancies/1"},
		{"application", "prepare", "1000000000", "--text-file", "missing"},
		{"application", "submit", "--send"},
		{"profile", "get", "--timeout", "0s"},
		{"resume", "delete", "synthetic"},
		{"resumes", "list"},
		{"profile", "specialization", "update"},
	} {
		var stdout bytes.Buffer
		if err := cli.Run(t.Context(), args, &stdout, &bytes.Buffer{}); err == nil || stdout.Len() != 0 {
			t.Fatalf("invalid %v emitted success: %v %s", args, err, stdout.String())
		}
	}
	output := filepath.Join(t.TempDir(), "status.json")
	var stdout bytes.Buffer
	if err := cli.Run(t.Context(), []string{"auth", "status", "--output", output, "--session-file", missing}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(output)
	if err != nil || !json.Valid(b) || stdout.Len() != 0 || !strings.Contains(string(b), `"session_available": false`) {
		t.Fatalf("private output = %s %v", b, err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("output permissions: %v %v", info, err)
	}
}
