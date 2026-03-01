package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/gurkankaymak/hocon"
)

// configToFlag maps HOCON config paths to CLI flag names.
var configToFlag = map[string]string{
	"interface":              "interface",
	"port":                   "port",
	"max-buffer-duration":    "max-buffer-duration",
	"journal.dir":            "journal-dir",
	"journal.prefix":         "journal-prefix",
	"journal.block-size":     "journal-block-size",
	"journal.compression":    "journal-compression",
	"journal.rotate.duration": "journal-rotate-duration",
	"journal.rotate.size":    "journal-rotate-size",
}

// findConfigFile resolves which config file to use.
// If configFlag is non-empty, that exact path is required (error if missing).
// Otherwise, searches ./lplex.conf then /etc/lplex/lplex.conf.
// Returns "" with no error if no config file is found.
func findConfigFile(configFlag string) (string, error) {
	if configFlag != "" {
		if _, err := os.Stat(configFlag); err != nil {
			return "", fmt.Errorf("config file not found: %s", configFlag)
		}
		return configFlag, nil
	}

	for _, path := range []string{"./lplex.conf", "/etc/lplex/lplex.conf"} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", nil
}

// applyConfig parses a HOCON config file and sets any flag values that
// weren't explicitly provided on the command line. CLI flags always win.
func applyConfig(path string) error {
	cfg, err := hocon.ParseResource(path)
	if err != nil {
		return fmt.Errorf("parsing config %s: %w", path, err)
	}

	// Collect flags the user explicitly set on the command line.
	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		explicit[f.Name] = true
	})

	for configKey, flagName := range configToFlag {
		if explicit[flagName] {
			continue
		}
		val := cfg.GetString(configKey)
		if val == "" {
			continue
		}
		if err := flag.Set(flagName, val); err != nil {
			return fmt.Errorf("config key %q (flag -%s): %w", configKey, flagName, err)
		}
	}

	return nil
}
