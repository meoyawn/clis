package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/meoyawn/clis/glassdoor/internal/service"
)

func TestHumanChallengeOpensDefaultBrowserAndReturns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell opener fixture targets macOS/Linux")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "opened")
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := os.WriteFile(filepath.Join(dir, opener), []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$SALARY_TEST_OPEN_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("SALARY_TEST_OPEN_LOG", log)
	var out bytes.Buffer
	reportError(context.Background(), &service.HumanVerificationError{StatusCode: 403}, &out)
	opened, err := os.ReadFile(log)
	if err != nil || string(opened) != "https://www.glassdoor.com/Salaries/index.htm\n" {
		t.Fatalf("challenge did not open exactly one trusted URL: %q, %v", opened, err)
	}
	if !strings.Contains(out.String(), "Complete") || !strings.Contains(out.String(), "auth login") {
		t.Fatalf("missing human recovery instructions: %s", out.String())
	}
	reportError(context.Background(), errors.New("HTTP 429; stopped"), &out)
	after, err := os.ReadFile(log)
	if err != nil || string(after) != string(opened) {
		t.Fatal("an ordinary error opened another browser tab")
	}
}
