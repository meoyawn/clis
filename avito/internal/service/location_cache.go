package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LocationCache struct{ Dir string }

func DefaultLocationCache() (LocationCache, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return LocationCache{}, fmt.Errorf("locate city cache: %w", err)
	}
	return LocationCache{Dir: filepath.Join(dir, "avito", "locations")}, nil
}

type cachedLocation struct {
	City string `json:"city"`
	ID   int64  `json:"id"`
}

func (cache LocationCache) Resolve(ctx context.Context, client *Client, city string, auth RequestOptions) (Location, int, error) {
	if err := ctx.Err(); err != nil {
		return Location{}, 0, err
	}
	if err := ValidateCity(city); err != nil {
		return Location{}, 0, err
	}
	city = strings.TrimSpace(city)
	if cache.Dir == "" {
		return Location{}, 0, fmt.Errorf("city cache directory must not be empty")
	}
	key := sha256.Sum256([]byte(strings.ToLower(city)))
	path := filepath.Join(cache.Dir, fmt.Sprintf("%x.json", key))
	location, err := loadLocation(path, city)
	if err == nil {
		return location, 0, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Location{}, 0, err
	}
	if client == nil {
		return Location{}, 0, fmt.Errorf("city lookup client must not be nil for uncached city %q", city)
	}
	location, err = client.ResolveCity(ctx, city, auth)
	if err != nil {
		return Location{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return Location{}, 1, err
	}
	if err := os.MkdirAll(cache.Dir, 0700); err != nil {
		return Location{}, 1, fmt.Errorf("create city cache directory: %w", err)
	}
	data, err := json.Marshal(cachedLocation{City: location.Name, ID: location.ID})
	if err != nil {
		return Location{}, 1, fmt.Errorf("encode city cache: %w", err)
	}
	if err := writeAtomic(path, data); err != nil {
		return Location{}, 1, fmt.Errorf("save city cache: %w", err)
	}
	return location, 1, nil
}

func loadLocation(path, city string) (Location, error) {
	f, err := os.Open(path) //nolint:gosec // G304: Resolve constructs this path from a SHA-256 filename under the cache directory.
	if err != nil {
		return Location{}, fmt.Errorf("read city cache: %w", err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return Location{}, fmt.Errorf("read city cache: %w", err)
	}
	var record cachedLocation
	if len(data) > 4096 || json.Unmarshal(data, &record) != nil || record.ID <= 0 || !strings.EqualFold(record.City, city) {
		return Location{}, fmt.Errorf("invalid city cache %q; remove this file to resolve the city again", path)
	}
	return Location{ID: record.ID, Name: record.City}, nil
}
