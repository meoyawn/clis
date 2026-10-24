package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Config struct {
	City          string `json:"city"`
	LocalPriority bool   `json:"local-priority"`
	Timeout       string `json:"timeout"`
}

func DefaultConfig() Config {
	return Config{
		LocalPriority: true, Timeout: "60s",
	}
}

func (config Config) Validate() error {
	if config.City != "" {
		if err := ValidateCity(config.City); err != nil {
			return err
		}
	}
	timeout, err := time.ParseDuration(config.Timeout)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("timeout %q must be a positive duration, for example 60s", config.Timeout)
	}
	return nil
}

type ConfigStore struct{ Path string }

func DefaultConfigStore() (ConfigStore, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ConfigStore{}, fmt.Errorf("locate user config: %w", err)
	}
	return ConfigStore{Path: filepath.Join(dir, "avito", "config.json")}, nil
}

func (store ConfigStore) Load() (Config, error) {
	config := DefaultConfig()
	if store.Path == "" {
		return Config{}, fmt.Errorf("config path must not be empty")
	}
	f, err := os.Open(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer f.Close() //nolint:errcheck
	const maxConfigBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if len(data) > maxConfigBytes || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return Config{}, fmt.Errorf("config %q must be a JSON object of at most %d bytes", store.Path, maxConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", store.Path, err)
	}
	if !errors.Is(decoder.Decode(new(any)), io.EOF) {
		return Config{}, fmt.Errorf("config %q must contain exactly one JSON object", store.Path)
	}
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config %q: %w", store.Path, err)
	}
	return config, nil
}

func (store ConfigStore) Save(config Config) error {
	if store.Path == "" {
		return fmt.Errorf("config path must not be empty")
	}
	if err := config.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	dir := filepath.Dir(store.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("config directory %q must be a directory, not a symlink", dir)
	}
	if err := os.Chmod(dir, 0700); err != nil { //nolint:gosec // G302: the owner needs execute permission to traverse this private directory.
		return fmt.Errorf("restrict config directory permissions: %w", err)
	}
	if err := writeAtomic(store.Path, data); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}
