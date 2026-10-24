package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type appPaths struct{ config, index string }

func defaultPaths() (appPaths, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return appPaths{}, fmt.Errorf("locate user config: %w", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return appPaths{}, fmt.Errorf("locate user cache: %w", err)
	}
	return appPaths{config: filepath.Join(config, "invitro", "config.json"), index: filepath.Join(cache, "invitro", "products")}, nil
}

type productLocation struct {
	ID        string `json:"id"`
	CatalogID string `json:"catalog_id"`
	City      string `json:"city"`
	Type      string `json:"type"`
	URL       string `json:"url"`
}

func (p appPaths) remember(city string, products []product) error {
	if err := validateCity(city); err != nil {
		return err
	}
	for _, item := range products {
		if (!validID.MatchString(item.ID) && !uuidPattern.MatchString(item.ID)) || !uuidPattern.MatchString(item.CatalogID) {
			return fmt.Errorf("invalid catalog identity %q / %q", item.ID, item.CatalogID)
		}
		entry := productLocation{ID: item.ID, CatalogID: item.CatalogID, City: city, Type: item.Type, URL: item.URL}
		ids := []string{item.ID}
		if item.CatalogID != item.ID {
			ids = append(ids, item.CatalogID)
		}
		for _, id := range ids {
			if err := atomicJSON(filepath.Join(p.index, city, id+".json"), entry); err != nil {
				return fmt.Errorf("remember product %s: %w", id, err)
			}
		}
	}
	return nil
}

func (p appPaths) lookup(city, id string) (*productLocation, error) {
	if err := validateCity(city); err != nil {
		return nil, err
	}
	if !validID.MatchString(id) && !uuidPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid product id %q", id)
	}
	path := filepath.Join(p.index, city, id+".json")
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect cached product %q: %w", id, err)
	}
	if info.Size() > 65536 {
		return nil, fmt.Errorf("cached product %q exceeds 64 KiB", id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cached product %q: %w", id, err)
	}
	var entry productLocation
	if json.Unmarshal(data, &entry) != nil || entry.City != city || (entry.ID != id && entry.CatalogID != id) {
		return nil, fmt.Errorf("invalid cached product %q; repeat invitro search to refresh it", id)
	}
	return &entry, nil
}

// Temp-file + rename prevents readers from seeing partial JSON during concurrent
// config writes and searches. Preferences and the URL index are owner-only files.
func atomicJSON(path string, value any) (err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode local state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil { // #nosec G703 -- Local config/cache paths; product city and ID components are validated before use.
		return fmt.Errorf("create state directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".invitro-*")
	if err != nil {
		return fmt.Errorf("create state file: %w", err)
	}
	tmp := f.Name()
	defer func() {
		if removeErr := os.Remove(tmp); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) { // #nosec G703 -- Remove only the filename returned by os.CreateTemp above.
			err = errors.Join(err, fmt.Errorf("remove temporary state file: %w", removeErr))
		}
	}()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write state: %w", errors.Join(err, f.Close()))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil { // #nosec G703 -- Destination is the local preferences file or a validated product cache path.
		return fmt.Errorf("replace state: %w", err)
	}
	return nil
}
