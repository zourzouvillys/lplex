package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

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
	"device.idle-timeout":            "device-idle-timeout",
	"send.enabled":                   "send-enabled",
	"virtual-device.enabled":                "virtual-device",
	"virtual-device.name":                   "virtual-device-name",
	"virtual-device.model-id":               "virtual-device-model-id",
	"virtual-device.claim-heartbeat":         "virtual-device-claim-heartbeat",
	"virtual-device.product-info-heartbeat":  "virtual-device-product-info-heartbeat",
	"bus-silence-timeout":        "bus-silence-timeout",
	"replication.target":         "replication-target",
	"replication.instance-id":    "replication-instance-id",
	"replication.tls.cert":       "replication-tls-cert",
	"replication.tls.key":        "replication-tls-key",
	"replication.tls.ca":                    "replication-tls-ca",
	"replication.max-live-lag":               "replication-max-live-lag",
	"replication.lag-check-interval":         "replication-lag-check-interval",
	"replication.min-lag-reconnect-interval": "replication-min-lag-reconnect-interval",
	"health.bus-silence-threshold":           "bus-silence-threshold",
}

// findConfigFile resolves which config file to use.
// If configFlag is non-empty, that exact path is required (error if missing).
// Otherwise, searches ./lplex-server.conf then /etc/lplex/lplex-server.conf,
// falling back to ./lplex.conf then /etc/lplex/lplex.conf for backward compat.
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

	for _, path := range []string{
		"./lplex-server.conf", "/etc/lplex/lplex-server.conf",
		"./lplex.conf", "/etc/lplex/lplex.conf",
	} {
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

	// Handle send.rules: supports both string elements (DSL syntax) and
	// object elements ({ deny, pgn, name }) in the same array.
	if !explicit["send-rules"] {
		if arr := cfg.GetArray("send.rules"); len(arr) > 0 {
			parts := make([]string, 0, len(arr))
			for i, elem := range arr {
				switch elem.Type() {
				case hocon.StringType:
					parts = append(parts, string(elem.(hocon.String)))
				case hocon.ObjectType:
					dsl, err := hoconRuleToDSL(elem.(hocon.Object))
					if err != nil {
						return fmt.Errorf("config key send.rules[%d]: %w", i, err)
					}
					parts = append(parts, dsl)
				default:
					return fmt.Errorf("config key send.rules[%d]: expected string or object, got %v", i, elem.Type())
				}
			}
			if err := flag.Set("send-rules", strings.Join(parts, ";")); err != nil {
				return fmt.Errorf("config key send.rules: %w", err)
			}
		}
	}

	return nil
}

// hoconRuleToDSL converts a HOCON object rule to a DSL string.
// Supported fields: deny (bool), pgn (string), name (string or string array).
func hoconRuleToDSL(obj hocon.Object) (string, error) {
	var parts []string

	if v, ok := obj["deny"]; ok {
		if bool(v.(hocon.Boolean)) {
			parts = append(parts, "!")
		}
	}

	if v, ok := obj["pgn"]; ok {
		parts = append(parts, "pgn:"+string(v.(hocon.String)))
	}

	if v, ok := obj["name"]; ok {
		switch v.Type() {
		case hocon.StringType:
			parts = append(parts, "name:"+string(v.(hocon.String)))
		case hocon.ArrayType:
			arr := v.(hocon.Array)
			names := make([]string, len(arr))
			for i, n := range arr {
				names[i] = string(n.(hocon.String))
			}
			parts = append(parts, "name:"+strings.Join(names, ","))
		default:
			return "", fmt.Errorf("name field must be a string or array")
		}
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("rule object must have at least one of: deny, pgn, name")
	}

	return strings.Join(parts, " "), nil
}
