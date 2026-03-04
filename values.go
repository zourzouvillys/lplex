package lplex

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"
)

// valueKey identifies a unique value slot: one per (source address, PGN) pair.
type valueKey struct {
	Source uint8
	PGN    uint32
}

// valueEntry is the most recent frame data for a given (source, PGN).
type valueEntry struct {
	Timestamp time.Time
	Data      []byte
	Seq       uint64
}

// ValueStore tracks the last-seen frame data for each (source, PGN) pair.
// The broker goroutine writes via Record; HTTP handlers read via Snapshot.
type ValueStore struct {
	mu     sync.RWMutex
	values map[valueKey]*valueEntry
}

// NewValueStore creates an empty value store.
func NewValueStore() *ValueStore {
	return &ValueStore{
		values: make(map[valueKey]*valueEntry),
	}
}

// Record updates the stored value for the given source and PGN.
// Called by the broker goroutine on every frame.
func (vs *ValueStore) Record(source uint8, pgn uint32, ts time.Time, data []byte, seq uint64) {
	key := valueKey{Source: source, PGN: pgn}

	vs.mu.Lock()
	entry := vs.values[key]
	if entry == nil {
		entry = &valueEntry{}
		vs.values[key] = entry
	}
	entry.Timestamp = ts
	entry.Data = append(entry.Data[:0], data...) // reuse backing array
	entry.Seq = seq
	vs.mu.Unlock()
}

// PGNValue is a single PGN's last-known value in the JSON response.
type PGNValue struct {
	PGN  uint32 `json:"pgn"`
	Ts   string `json:"ts"`
	Data string `json:"data"`
	Seq  uint64 `json:"seq"`
}

// DeviceValues groups PGN values by device in the JSON response.
type DeviceValues struct {
	Name         string     `json:"name"`
	Source       uint8      `json:"src"`
	Manufacturer string     `json:"manufacturer,omitempty"`
	ModelID      string     `json:"model_id,omitempty"`
	Values       []PGNValue `json:"values"`
}

// Snapshot returns the current values grouped by device, resolved against
// the device registry for NAME and manufacturer info.
func (vs *ValueStore) Snapshot(devices *DeviceRegistry) []DeviceValues {
	// Snapshot the values under RLock, then release before touching the device registry.
	vs.mu.RLock()
	type entry struct {
		key valueKey
		val valueEntry
	}
	entries := make([]entry, 0, len(vs.values))
	for k, v := range vs.values {
		entries = append(entries, entry{key: k, val: *v})
	}
	vs.mu.RUnlock()

	// Group by source address.
	bySource := make(map[uint8][]PGNValue)
	sources := make(map[uint8]struct{})
	for _, e := range entries {
		sources[e.key.Source] = struct{}{}
		bySource[e.key.Source] = append(bySource[e.key.Source], PGNValue{
			PGN:  e.key.PGN,
			Ts:   e.val.Timestamp.UTC().Format(time.RFC3339Nano),
			Data: hex.EncodeToString(e.val.Data),
			Seq:  e.val.Seq,
		})
	}

	// Build sorted source list.
	sortedSources := make([]uint8, 0, len(sources))
	for src := range sources {
		sortedSources = append(sortedSources, src)
	}
	slices.Sort(sortedSources)

	result := make([]DeviceValues, 0, len(sortedSources))
	for _, src := range sortedSources {
		vals := bySource[src]
		slices.SortFunc(vals, func(a, b PGNValue) int {
			if a.PGN < b.PGN {
				return -1
			}
			if a.PGN > b.PGN {
				return 1
			}
			return 0
		})

		dv := DeviceValues{
			Source: src,
			Values: vals,
		}

		// Resolve device identity from the registry.
		if dev := devices.Get(src); dev != nil && dev.NAME != 0 {
			dv.Name = fmt.Sprintf("0x%016x", dev.NAME)
			dv.Manufacturer = dev.Manufacturer
			dv.ModelID = dev.ModelID
		}

		result = append(result, dv)
	}

	return result
}

// SnapshotJSON returns the snapshot as pre-serialized JSON.
func (vs *ValueStore) SnapshotJSON(devices *DeviceRegistry) json.RawMessage {
	snap := vs.Snapshot(devices)
	b, _ := json.Marshal(snap)
	return b
}
