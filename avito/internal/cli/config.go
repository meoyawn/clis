package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	avito "github.com/meoyawn/clis/avito/internal/service"
)

func configFlags(config *avito.Config) *flag.FlagSet {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.City, "city", config.City, "Russian city name")
	flags.BoolVar(&config.LocalPriority, "local-priority", config.LocalPriority, "selected city first")
	flags.StringVar(&config.Timeout, "timeout", config.Timeout, "request timeout")
	return flags
}

func applySavedConfig(flags *flag.FlagSet, store avito.ConfigStore) error {
	config, err := store.Load()
	if err != nil {
		return err
	}
	explicit := make(map[string]bool)
	flags.Visit(func(option *flag.Flag) { explicit[option.Name] = true })
	var applyErr error
	configFlags(&config).VisitAll(func(option *flag.Flag) {
		if flags.Lookup(option.Name) == nil || explicit[option.Name] || applyErr != nil {
			return
		}
		if option.Name == "city" && explicit["location-id"] {
			return
		}
		applyErr = flags.Set(option.Name, option.Value.String())
	})
	return applyErr
}

func runConfig(ctx context.Context, args []string, stdout io.Writer, setup setupFunc, store avito.ConfigStore) error {
	if len(args) > 0 {
		command := args[0]
		if len(args) == 1 && (command == "--help" || command == "-h" || command == "help") {
			_, err := io.WriteString(stdout, usage)
			return err
		}
		if len(args) != 3 || command != "set" {
			return fmt.Errorf("config expects no arguments or: avito config set <key> <value>")
		}
	}
	config, err := store.Load()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return writeJSON(stdout, config)
	}
	flags := configFlags(&config)
	key, value := args[1], args[2]
	if flags.Lookup(key) == nil {
		return fmt.Errorf("unknown config key %q; use city, local-priority, or timeout", key)
	}
	if err := flags.Set(key, value); err != nil {
		return fmt.Errorf("set config %s: %w", key, err)
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if key == "city" {
		if err := avito.ValidateCity(config.City); err != nil {
			return err
		}
		deps, err := setup()
		if err != nil {
			return err
		}
		if deps.close != nil {
			defer deps.close()
		}
		timeout, err := time.ParseDuration(config.Timeout)
		if err != nil {
			return fmt.Errorf("parse config timeout %q: %w", config.Timeout, err)
		}
		lookupCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		location, _, err := deps.locations.Resolve(lookupCtx, deps.client, config.City, avito.RequestOptions{})
		if err != nil {
			return err
		}
		config.City = location.Name
		if err := lookupCtx.Err(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.Save(config); err != nil {
		return err
	}
	return writeJSON(stdout, config)
}
