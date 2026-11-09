package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestChatApprovalBindsSenderRecipientAndExactText(t *testing.T) {
	hash := chatPackageHash("42", "99", "hello\n")
	for _, changed := range []string{
		chatPackageHash("43", "99", "hello\n"),
		chatPackageHash("42", "98", "hello\n"),
		chatPackageHash("42", "99", "hello"),
	} {
		if changed == hash {
			t.Fatal("approval hash did not bind the exact package")
		}
	}
	var output bytes.Buffer
	err := chatCommand(context.Background(), []string{"--text-file", "unused.txt", "--send", "recipient"}, t.TempDir(), &output, &output)
	if err == nil || !strings.Contains(err.Error(), "--expect-account") {
		t.Fatalf("unreviewed send did not stop before loading credentials: %v", err)
	}
}

func TestChatReadbackOnlyAllowsRemovalOfTerminalLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		expected string
		actual   string
		matches  bool
	}{
		{"hello\n", "hello\n", true},
		{"hello\r\n", "hello", true},
		{"first\nsecond\n", "first\nsecond", true},
		{"hello ", "hello", false},
		{" hello\n", "hello", false},
		{"first\nsecond\n", "first second", false},
		{"hello", "hello\n", false},
		{"hello!\n", "hello", false},
	} {
		if got := chatReadbackMatches(tc.expected, tc.actual); got != tc.matches {
			t.Fatalf("readbackMatches(%q, %q)=%t; want %t", tc.expected, tc.actual, got, tc.matches)
		}
	}
}
