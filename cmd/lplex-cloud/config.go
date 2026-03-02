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
	"grpc.listen":       "grpc-listen",
	"grpc.tls.cert":     "tls-cert",
	"grpc.tls.key":      "tls-key",
	"grpc.tls.client-ca": "tls-client-ca",
	"http.listen":       "http-listen",
	"data-dir":          "data-dir",
}

// findConfigFile resolves which config file to use.
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

	for _, path := range []string{"./lplex-cloud.conf", "/etc/lplex-cloud/lplex-cloud.conf"} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", nil
}

// applyConfig parses a HOCON config file and sets any flag values that
// weren't explicitly provided on the command line.
func applyConfig(path string) error {
	cfg, err := hocon.ParseResource(path)
	if err != nil {
		return fmt.Errorf("parsing config %s: %w", path, err)
	}

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
