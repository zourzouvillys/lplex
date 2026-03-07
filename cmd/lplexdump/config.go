package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gurkankaymak/hocon"
)

// BoatConfig holds the connection settings for a named boat.
type BoatConfig struct {
	Name  string // config key, e.g. "sv-dockwise"
	MDNS  string // mDNS instance name to search for, e.g. "inuc1"
	Cloud string // cloud fallback URL, e.g. "https://lplex.dockwise.app/instances/sv-dockwise"
}

// defaultConfigPath returns ~/.config/lplex/lplexdump.conf.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "lplex", "lplexdump.conf")
}

// loadBoatConfig reads the HOCON config file and returns all boats defined
// under the "boats" key. Returns an empty map (no error) if the file doesn't exist.
func loadBoatConfig(path string) (map[string]BoatConfig, error) {
	if path == "" {
		return nil, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("checking config %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("config path is a directory: %s", path)
	}

	cfg, err := hocon.ParseResource(path)
	if err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	boatsObj := cfg.GetObject("boats")
	if boatsObj == nil {
		return nil, nil
	}

	boats := make(map[string]BoatConfig, len(boatsObj))
	for name := range boatsObj {
		bc := BoatConfig{Name: name}

		if v := cfg.GetString("boats." + name + ".mdns"); v != "" {
			bc.MDNS = v
		}
		if v := cfg.GetString("boats." + name + ".cloud"); v != "" {
			bc.Cloud = v
		}
		if bc.MDNS == "" && bc.Cloud == "" {
			return nil, fmt.Errorf("boat %q must have at least one of mdns or cloud", name)
		}
		boats[name] = bc
	}

	return boats, nil
}

// resolveBoat picks the right boat config. If name is empty and there's exactly
// one boat defined, it auto-selects it.
func resolveBoat(name string, boats map[string]BoatConfig) (BoatConfig, error) {
	if len(boats) == 0 {
		return BoatConfig{}, fmt.Errorf("no boats configured in config file")
	}

	if name == "" {
		if len(boats) == 1 {
			for _, bc := range boats {
				return bc, nil
			}
		}
		names := make([]string, 0, len(boats))
		for n := range boats {
			names = append(names, n)
		}
		return BoatConfig{}, fmt.Errorf("multiple boats configured, specify one with -boat: %v", names)
	}

	bc, ok := boats[name]
	if !ok {
		names := make([]string, 0, len(boats))
		for n := range boats {
			names = append(names, n)
		}
		return BoatConfig{}, fmt.Errorf("boat %q not found in config, available: %v", name, names)
	}
	return bc, nil
}
