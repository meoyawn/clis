package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicURLIndex(t *testing.T) {
	t.Parallel()

	index := URLIndex{Dir: filepath.Join(t.TempDir(), "items")}
	item := Listing{ID: "1000000002", URL: WebOrigin + "/kazan/audio_i_video/tv_1000000002?context=discard", Title: "not persisted"}
	if err := index.Remember([]Listing{item}); err != nil {
		t.Fatal(err)
	}
	ref, err := index.Lookup(item.ID)
	if err != nil || strings.Contains(ref.URL, "?") {
		t.Fatalf("lookup failed: %+v %v", ref, err)
	}
	path := filepath.Join(index.Dir, item.ID+".json")
	data, readErr := os.ReadFile(path) //nolint:gosec
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(data), "not persisted") || strings.Contains(string(data), "context") {
		t.Fatal("index stored response data or tracking context")
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("index permissions %o", info.Mode().Perm())
		}
	}
	if err := os.WriteFile(path, []byte(`{"id":"1000000002","url":"https://outside.test/kazan/audio/tv_1000000002"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Lookup(item.ID); err == nil {
		t.Fatal("accepted poisoned URL index")
	}
}
