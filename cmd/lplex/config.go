package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/gurkankaymak/hocon"
)

// configToFlag maps HOCON config paths to CLI flag names.
var configToFlag = map[string]string{
	"interface":               "interface",
	"port":                    "port",
	"max-buffer-duration":     "max-buffer-duration",
	"journal.dir":             "journal-dir",
	"journal.prefix":          "journal-prefix",
	"journal.block-size":      "journal-block-size",
	"journal.compression":     "journal-compression",
	"journal.rotate.duration": "journal-rotate-duration",
	"journal.rotate.size":     "journal-rotate-size",
	"journal.retention.max-age":         "journal-retention-max-age",
	"journal.retention.min-keep":        "journal-retention-min-keep",
	"journal.retention.max-size":        "journal-retention-max-size",
	"journal.retention.soft-pct":        "journal-retention-soft-pct",
	"journal.retention.overflow-policy": "journal-retention-overflow-policy",
	"journal.archive.command":           "journal-archive-command",
	"journal.archive.trigger":           "journal-archive-trigger",
	"replication.target":         "replication-target",
	"replication.instance-id":    "replication-instance-id",
	"replication.tls.cert":       "replication-tls-cert",
	"replication.tls.key":        "replication-tls-key",
	"replication.tls.ca":         "replication-tls-ca",
	"health.bus-silence-threshold": "bus-silence-threshold",
}

// findConfigFile resolves which config file to use.
// If configFlag is non-empty, that exact path is required (error if missing).
// Otherwise, searches ./lplex.conf then /etc/lplex/lplex.conf.
// Returns "" with no error if no config file is found.
func findConfigFile(configFlag string) (string, error) {
	if configFlag != "" {
		info, err := os.Stat(configFlag)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("config file not found: %s", configFlag)
			}

			return "", fmt.Errorf("checking config file %s: %w", configFlag, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("config path is a directory: %s", configFlag)
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
