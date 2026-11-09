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
