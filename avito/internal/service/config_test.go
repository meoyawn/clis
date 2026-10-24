package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDefaultsAndPersistentSettings(t *testing.T) {
	t.Parallel()

	store := ConfigStore{Path: filepath.Join(t.TempDir(), "avito", "config.json")}
	config, err := store.Load()
	if err != nil || config != DefaultConfig() || config.City != "" {
		t.Fatalf("missing config defaults: %+v, %v", config, err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatal("reading defaults created a config file")
	}
	config.City, config.Timeout = "Москва", "2s"
	if err := store.Save(config); err != nil {
		t.Fatal(err)
	}
	loaded, err := (ConfigStore{Path: store.Path}).Load()
	if err != nil || loaded != config {
		t.Fatalf("config did not survive a new store: %+v, %v", loaded, err)
	}
	for path, mode := range map[string]os.FileMode{store.Path: 0600, filepath.Dir(store.Path): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("incorrect permissions for %s: %v", path, err)
		}
	}
	before, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	config.Timeout = "0s"
	if err := store.Save(config); err == nil {
		t.Fatal("saved invalid config")
	}
	after, readErr := os.ReadFile(store.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid update changed saved config")
	}
	files, readErr := os.ReadDir(filepath.Dir(store.Path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(files) != 1 {
		t.Fatal("config save left temporary files")
	}
}

func TestInvalidConfigFailsInsteadOfUsingDefaults(t *testing.T) {
	t.Parallel()

	for _, data := range []string{
		`null`, `[]`, `{`, `{} {}`, `{"unknown":1}`, `{"limit":10}`, `{"page":1}`,
		`{"location-id":650400}`, `{"category-id":32}`, `{"timeout":"0s"}`,
		`{"timeout":"bad"}`, `{"user-agent":"custom-agent"}`, `{"city":"  "}`,
		`{"local-priority":"yes"}`, strings.Repeat(" ", 64<<10) + `{}`,
	} {
		t.Run(truncate(data, 50), func(t *testing.T) {
			t.Parallel()

			store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
			if err := os.WriteFile(store.Path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(); err == nil {
				t.Fatal("silently accepted invalid config")
			}
		})
	}
}
