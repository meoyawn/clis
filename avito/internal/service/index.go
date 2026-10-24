package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type URLIndex struct{ Dir string }

func DefaultURLIndex() (URLIndex, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return URLIndex{}, fmt.Errorf("locate URL index: %w", err)
	}
	return URLIndex{Dir: filepath.Join(cache, "avito", "items")}, nil
}

type savedURL struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (index URLIndex) Remember(listings []Listing) error {
	for _, item := range listings {
		if item.URL == "" {
			continue
		}
		canonical, err := canonicalListingURL(item.URL, item.ID)
		if err != nil {
			return fmt.Errorf("remember listing %s: %w", item.ID, err)
		}
		if err := os.MkdirAll(index.Dir, 0700); err != nil {
			return fmt.Errorf("create URL index: %w", err)
		}
		data, err := json.Marshal(savedURL{ID: item.ID, URL: canonical})
		if err != nil {
			return fmt.Errorf("encode URL index: %w", err)
		}
		if err := writeAtomic(filepath.Join(index.Dir, item.ID+".json"), data); err != nil {
			return err
		}
	}
	return nil
}

func (index URLIndex) Lookup(id string) (ItemReference, error) {
	if !numericID.MatchString(id) {
		return ItemReference{}, fmt.Errorf("invalid listing ID %q", id)
	}
	f, err := os.Open(filepath.Join(index.Dir, id+".json")) //nolint:gosec // G304: numericID restricts id to decimal digits before it becomes a filename.
	if errors.Is(err, os.ErrNotExist) {
		return ItemReference{}, fmt.Errorf("no canonical URL saved for listing %s; pass its full Avito URL to show, or run web search first", id)
	}
	if err != nil {
		return ItemReference{}, fmt.Errorf("read listing %s URL: %w", id, err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil {
		return ItemReference{}, fmt.Errorf("read URL index: %w", err)
	}
	var record savedURL
	if len(data) > 8192 || json.Unmarshal(data, &record) != nil || record.ID != id {
		return ItemReference{}, fmt.Errorf("invalid URL index record for listing %s; pass the full Avito URL", id)
	}
	canonical, err := canonicalListingURL(record.URL, id)
	if err != nil {
		return ItemReference{}, fmt.Errorf("invalid saved URL for listing %s: %w", id, err)
	}
	return ItemReference{ID: id, URL: canonical}, nil
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".avito-*")
	if err != nil {
		return fmt.Errorf("create state file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) //nolint:errcheck
	if _, err := f.Write(append(data, '\n')); err != nil {
		return errors.Join(fmt.Errorf("write state file: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync state file: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}
