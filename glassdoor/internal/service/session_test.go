package service

import (
	"context"
	"strings"
	"testing"
)

func TestImportSessionRequiresRunningBrowserHeaders(t *testing.T) {
	// Cancellation prevents the old fallback from reading the local browser's
	// cookies. Missing input must be rejected before any browser inspection.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session, err := ImportSession(ctx, "Default", "")
	if session != nil || err == nil || !strings.Contains(err.Error(), "--headers-file is required") {
		t.Fatalf("missing running-browser headers must be rejected: session=%v err=%v", session != nil, err)
	}
}
