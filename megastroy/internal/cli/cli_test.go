package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
		{[]string{"image", "--help"}, ""},
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
		{[]string{"image"}, "exactly one URL"},
		{[]string{"image", "https://example.com/products/123.jpg"}, "invalid image URL"},
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
