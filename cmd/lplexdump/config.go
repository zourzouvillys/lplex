package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gurkankaymak/hocon"
)

// BoatConfig holds the connection settings for a named boat.
type BoatConfig struct {
	Name        string   // config key, e.g. "sv-dockwise"
	MDNS        string   // mDNS instance name to search for, e.g. "inuc1"
	Cloud       string   // cloud fallback URL, e.g. "https://lplex.dockwise.app/instances/sv-dockwise"
	ExcludePGNs []uint32 // PGNs to exclude for this boat (additive with global)
}

// DumpConfig holds global settings from the config file.
type DumpConfig struct {
	Boats       map[string]BoatConfig
	MDNSTimeout time.Duration // 0 means use default (3s)
	ExcludePGNs []uint32      // PGNs to exclude globally (all boats)
}

// defaultConfigPath returns ~/.config/lplex/lplexdump.conf.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "lplex", "lplexdump.conf")
}

// loadConfig reads the HOCON config file and returns the parsed config.
// Returns a zero DumpConfig (no error) if the file doesn't exist.
func loadConfig(path string) (DumpConfig, error) {
	if path == "" {
		return DumpConfig{}, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DumpConfig{}, nil
		}
		return DumpConfig{}, fmt.Errorf("checking config %s: %w", path, err)
	}
	if info.IsDir() {
		return DumpConfig{}, fmt.Errorf("config path is a directory: %s", path)
	}

	cfg, err := hocon.ParseResource(path)
	if err != nil {
		return DumpConfig{}, fmt.Errorf("parsing config %s: %w", path, err)
	}

	var dc DumpConfig

	// Parse mdns-timeout (e.g. "5s", "500ms").
	if v := getString(cfg, "mdns-timeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return DumpConfig{}, fmt.Errorf("config mdns-timeout: %w", err)
		}
		dc.MDNSTimeout = d
	}

	// Parse global exclude-pgn list.
	ep, err := getUint32Array(cfg, "exclude-pgn")
	if err != nil {
		return DumpConfig{}, fmt.Errorf("config exclude-pgn: %w", err)
	}
	dc.ExcludePGNs = ep

	// Parse boats.
	boatsObj := cfg.GetObject("boats")
	if boatsObj != nil {
		dc.Boats = make(map[string]BoatConfig, len(boatsObj))
		for name := range boatsObj {
			bc := BoatConfig{Name: name}

			if v := getString(cfg, "boats."+name+".mdns"); v != "" {
				bc.MDNS = v
			}
			if v := getString(cfg, "boats."+name+".cloud"); v != "" {
				bc.Cloud = v
			}
			if bc.MDNS == "" && bc.Cloud == "" {
				return DumpConfig{}, fmt.Errorf("boat %q must have at least one of mdns or cloud", name)
			}

			ep, err := getUint32Array(cfg, "boats."+name+".exclude-pgn")
			if err != nil {
				return DumpConfig{}, fmt.Errorf("boat %q exclude-pgn: %w", name, err)
			}
			bc.ExcludePGNs = ep

			dc.Boats[name] = bc
		}
	}

	return dc, nil
}

// getString extracts a raw string value from the HOCON config. We can't use
// cfg.GetString() because hocon.String.String() re-wraps values containing
// special characters (like :// in URLs) in literal double quotes.
func getString(cfg *hocon.Config, path string) string {
	v := cfg.Get(path)
	if v == nil {
		return ""
	}
	if s, ok := v.(hocon.String); ok {
		return strings.Trim(string(s), `"`)
	}
	return v.String()
}

// getUint32Array extracts an array of unsigned integers from the HOCON config.
// Supports both array syntax (exclude-pgn = [60928, 126996]) and single values
// (exclude-pgn = 60928).
func getUint32Array(cfg *hocon.Config, path string) ([]uint32, error) {
	v := cfg.Get(path)
	if v == nil {
		return nil, nil
	}
	switch v.Type() {
	case hocon.ArrayType:
		arr := v.(hocon.Array)
		result := make([]uint32, 0, len(arr))
		for _, elem := range arr {
			n, err := strconv.ParseUint(strings.TrimSpace(elem.String()), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid PGN %q: %w", elem.String(), err)
			}
			result = append(result, uint32(n))
		}
		return result, nil
	case hocon.NumberType, hocon.StringType:
		n, err := strconv.ParseUint(strings.TrimSpace(v.String()), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid PGN %q: %w", v.String(), err)
		}
		return []uint32{uint32(n)}, nil
	default:
		return nil, fmt.Errorf("expected number or array, got %v", v.Type())
	}
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
