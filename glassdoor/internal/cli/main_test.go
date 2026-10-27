package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/meoyawn/clis/glassdoor/internal/service"
)

func TestHumanChallengePrintsRecoveryURLWithoutOpeningBrowser(t *testing.T) {
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
	reportError(fmt.Errorf("salary request: %w", &service.HumanVerificationError{StatusCode: 403}), &out)
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CLI launched a browser instead of handing off the recovery URL: %v", err)
	}
	if !strings.Contains(out.String(), "https://www.glassdoor.com/Salaries/index.htm") || !strings.Contains(out.String(), "Chrome") || !strings.Contains(out.String(), "auth login") {
		t.Fatalf("missing Chrome recovery URL or session-import instructions: %s", out.String())
	}
	for _, message := range []string{"HTTP 403; stopped", "HTTP 429; stopped"} {
		out.Reset()
		reportError(errors.New(message), &out)
		if strings.Contains(out.String(), "https://") {
			t.Fatalf("ordinary access error incorrectly offered challenge recovery: %s", out.String())
		}
	}
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an ordinary error launched a browser")
	}
}
