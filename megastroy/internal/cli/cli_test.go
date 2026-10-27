package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	megastroy "github.com/meoyawn/clis/megastroy/internal/service"
)

func TestCLIHelpAndInvalidArguments(t *testing.T) {
	// An isolated configuration directory makes accidental auth work visible
	// without touching the user's browser or saved session.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, tt := range []struct {
		args []string
		err  string
	}{
		{[]string{"search", "--help"}, ""},
		{[]string{"details", "--help"}, ""},
		{[]string{"auth", "import", "--help"}, ""},
		{[]string{"search"}, "nonempty query"},
		{[]string{"search", "полотенцесушитель", "--page", "0"}, "page 0"},
		{[]string{"search", "--limit=-1", "полотенцесушитель"}, "limit -1"},
		{[]string{"search", "полотенцесушитель", "--sort", "unknown"}, "invalid sort"},
		{[]string{"search", "--filter", "q=other", "полотенцесушитель"}, "invalid filter"},
		{[]string{"search", "полотенцесушитель", "--filter", "price[from]=900", "--filter", "price[to]=100"}, "range"},
		{[]string{"search", "полотенцесушитель", "--category", "1102", "--filter", "category_id=255"}, "only be supplied once"},
		{[]string{"search", "полотенцесушитель", "--filter"}, "requires a value"},
		{[]string{"details", "123", "456"}, "exactly one"},
		{[]string{"image", "https://example.com/products/123.jpg"}, `unknown command "image"`},
		{[]string{"details", "123", "--timeout", "0s"}, "timeout 0s"},
		{[]string{"auth", "import", "--browser", "unknown"}, "invalid browser"},
		{[]string{"search", "--", "--help"}, "no usable saved"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := Run(context.Background(), tt.args, &stdout, &stderr)
			if tt.err == "" {
				if err != nil || !strings.Contains(stdout.String(), "Usage:") {
					t.Fatalf("help: output=%q err=%v", stdout.String(), err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.err) || stdout.Len() != 0 {
				t.Fatalf("want error %q and no success output, got %v %q", tt.err, err, stdout.String())
			}
		})
	}
}

func TestDDoSGuardURLHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell opener fixture targets macOS/Linux")
	}
	const target = "https://kazan.megastroy.com/catalog/search?category_id=1102&noredirect=1&q=Terminus&sort=price"
	ordinary := fmt.Errorf("HTTP 429; DDoS-Guard access unavailable")
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"blocked search", fmt.Errorf("catalog: %w", &megastroy.DDoSGuardError{URL: target}), target + "\n"},
		{"ordinary failure", ordinary, "megastroy: " + ordinary.Error() + "\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "opened")
			opener := "xdg-open"
			if runtime.GOOS == "darwin" {
				opener = "open"
			}
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$MEGASTROY_TEST_OPEN_LOG\"\n"
			if err := os.WriteFile(filepath.Join(dir, opener), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			t.Setenv("MEGASTROY_TEST_OPEN_LOG", log)
			var stderr bytes.Buffer
			reportError(tt.err, &stderr)
			if opened, err := os.ReadFile(log); !os.IsNotExist(err) {
				t.Fatalf("error reporting launched a browser: opened=%q err=%v", opened, err)
			}
			if stderr.String() != tt.want {
				t.Fatalf("error output: got %q, want %q", stderr.String(), tt.want)
			}
		})
	}
}
