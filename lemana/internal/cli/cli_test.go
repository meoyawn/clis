package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpAndValidationDoNotRequireBrowserSession(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"search", "смеситель", "--help"}, {"details", "--help"}, {"auth", "import", "--help"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "review_count") {
			t.Fatalf("incomplete help: %s", out.String())
		}
	}
	for _, args := range [][]string{{"search", "x", "--sort", "bogus"}, {"search", "x", "--page", "0"}, {"search", "x", "--filter", "broken"}, {"details"}, {"auth", "erase"}} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &out); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
	var out bytes.Buffer
	err := Run(context.Background(), []string{"auth", "status", "--session-file", filepath.Join(t.TempDir(), "missing")}, &out, &out)
	if err != nil || !strings.Contains(out.String(), `"imported": false`) {
		t.Fatalf("status err=%v output=%s", err, out.String())
	}
}
