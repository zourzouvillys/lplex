package lplex

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValueStoreRecordAndSnapshot(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(3, 129025, ts, []byte{0xaa, 0xbb}, 100)

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if snap[0].Source != 3 {
		t.Errorf("source: got %d, want 3", snap[0].Source)
	}
	if len(snap[0].Values) != 1 {
		t.Fatalf("expected 1 PGN value, got %d", len(snap[0].Values))
	}
	v := snap[0].Values[0]
	if v.PGN != 129025 {
		t.Errorf("pgn: got %d, want 129025", v.PGN)
	}
	if v.Data != "aabb" {
		t.Errorf("data: got %q, want %q", v.Data, "aabb")
	}
	if v.Seq != 100 {
		t.Errorf("seq: got %d, want 100", v.Seq)
	}
}

func TestValueStoreOverwrite(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	t0 := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)

	vs.Record(1, 129025, t0, []byte{0x01}, 1)
	vs.Record(1, 129025, t1, []byte{0x02}, 2)

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if len(snap[0].Values) != 1 {
		t.Fatalf("expected 1 PGN value (overwritten), got %d", len(snap[0].Values))
	}
	v := snap[0].Values[0]
	if v.Data != "02" {
		t.Errorf("data: got %q, want %q (should be latest)", v.Data, "02")
	}
	if v.Seq != 2 {
		t.Errorf("seq: got %d, want 2 (should be latest)", v.Seq)
	}
}

func TestValueStoreMultipleSourcesSamePGN(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(1, 129025, ts, []byte{0x01}, 1)
	vs.Record(5, 129025, ts, []byte{0x05}, 2)

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 2 {
		t.Fatalf("expected 2 device groups, got %d", len(snap))
	}
	// Should be sorted by source address.
	if snap[0].Source != 1 {
		t.Errorf("first device source: got %d, want 1", snap[0].Source)
	}
	if snap[1].Source != 5 {
		t.Errorf("second device source: got %d, want 5", snap[1].Source)
	}
}

func TestValueStoreDeviceResolution(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	// Register a Garmin device at source 3.
	claim := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21 // Garmin
	name |= uint64(12345)
	binary.LittleEndian.PutUint64(claim, name)
	reg.HandleAddressClaim(3, claim)

	prodPayload := buildProductInfoPayload(4242, "GPS 19x", "4.80", "1.0", "SN-001")
	reg.HandleProductInfo(3, prodPayload)

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(3, 129025, ts, []byte{0xaa}, 1)

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	dv := snap[0]
	if dv.Manufacturer != "Garmin" {
		t.Errorf("manufacturer: got %q, want Garmin", dv.Manufacturer)
	}
	if dv.ModelID != "GPS 19x" {
		t.Errorf("model_id: got %q, want %q", dv.ModelID, "GPS 19x")
	}
	if dv.Name == "" {
		t.Error("name should be set for known device")
	}
}

func TestValueStoreUnknownDevice(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(99, 129025, ts, []byte{0xff}, 1)

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	dv := snap[0]
	if dv.Name != "" {
		t.Errorf("name should be empty for unknown device, got %q", dv.Name)
	}
	if dv.Manufacturer != "" {
		t.Errorf("manufacturer should be empty for unknown device, got %q", dv.Manufacturer)
	}
	// Should still have values.
	if len(dv.Values) != 1 {
		t.Errorf("expected 1 value even for unknown device, got %d", len(dv.Values))
	}
}

func TestValueStoreEmptyReturnsEmptyArray(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	jsonBytes := vs.SnapshotJSON(reg, nil)
	if string(jsonBytes) != "[]" {
		t.Errorf("empty store should return [], got %s", string(jsonBytes))
	}

	// Also verify Snapshot returns non-nil empty slice.
	snap := vs.Snapshot(reg, nil)
	if snap == nil {
		t.Error("Snapshot should return non-nil slice")
	}
	if len(snap) != 0 {
		t.Errorf("expected 0 device groups, got %d", len(snap))
	}
}

func TestValueStoreMultiplePGNsSameSource(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(1, 129026, ts, []byte{0x01}, 1) // COG/SOG
	vs.Record(1, 129025, ts, []byte{0x02}, 2) // Position

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if len(snap[0].Values) != 2 {
		t.Fatalf("expected 2 PGN values, got %d", len(snap[0].Values))
	}
	// Values should be sorted by PGN.
	if snap[0].Values[0].PGN != 129025 {
		t.Errorf("first PGN: got %d, want 129025 (should be sorted)", snap[0].Values[0].PGN)
	}
	if snap[0].Values[1].PGN != 129026 {
		t.Errorf("second PGN: got %d, want 129026 (should be sorted)", snap[0].Values[1].PGN)
	}
}

func TestValueStoreSnapshotJSONShape(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(3, 129025, ts, []byte{0xaa, 0xbb, 0xcc, 0xdd}, 12345)

	jsonBytes := vs.SnapshotJSON(reg, nil)

	var result []DeviceValues
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 device, got %d", len(result))
	}
	if result[0].Values[0].Data != "aabbccdd" {
		t.Errorf("data: got %q, want aabbccdd", result[0].Values[0].Data)
	}
}

func TestValueStoreStatsOnlyDeviceShowsEmpty(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	// Device has RecordPacket (stats-only, NAME=0) but no address claim.
	reg.RecordPacket(42, time.Now(), 8)

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(42, 129025, ts, []byte{0x01}, 1)

	snap := vs.Snapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	// Stats-only entry has NAME=0, so no name/manufacturer resolved.
	if snap[0].Name != "" {
		t.Errorf("name should be empty for stats-only device, got %q", snap[0].Name)
	}
}

func TestValueStoreSnapshotFilterByPGN(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(1, 129025, ts, []byte{0x01}, 1) // Position
	vs.Record(1, 129026, ts, []byte{0x02}, 2) // COG/SOG
	vs.Record(2, 129025, ts, []byte{0x03}, 3) // Position from another device

	snap := vs.Snapshot(reg, &EventFilter{PGNs: []uint32{129025}})
	if len(snap) != 2 {
		t.Fatalf("expected 2 device groups, got %d", len(snap))
	}
	for _, dv := range snap {
		if len(dv.Values) != 1 {
			t.Fatalf("src %d: expected 1 value, got %d", dv.Source, len(dv.Values))
		}
		if dv.Values[0].PGN != 129025 {
			t.Errorf("src %d: expected PGN 129025, got %d", dv.Source, dv.Values[0].PGN)
		}
	}
}

func TestValueStoreSnapshotFilterByManufacturer(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	// Register Garmin at source 1.
	claim := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21 // Garmin manufacturer code
	name |= uint64(1)
	binary.LittleEndian.PutUint64(claim, name)
	reg.HandleAddressClaim(1, claim)

	// Register Simrad (code 1857) at source 2.
	claim2 := make([]byte, 8)
	var name2 uint64
	name2 |= uint64(1857) << 21
	name2 |= uint64(2)
	binary.LittleEndian.PutUint64(claim2, name2)
	reg.HandleAddressClaim(2, claim2)

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(1, 129025, ts, []byte{0x01}, 1)
	vs.Record(2, 129025, ts, []byte{0x02}, 2)

	snap := vs.Snapshot(reg, &EventFilter{Manufacturers: []string{"Garmin"}})
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if snap[0].Source != 1 {
		t.Errorf("expected source 1 (Garmin), got %d", snap[0].Source)
	}
}

func TestValueStoreSnapshotFilterByPGNAndManufacturer(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	// Garmin at source 1.
	claim := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21
	name |= uint64(1)
	binary.LittleEndian.PutUint64(claim, name)
	reg.HandleAddressClaim(1, claim)

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(1, 129025, ts, []byte{0x01}, 1)
	vs.Record(1, 129026, ts, []byte{0x02}, 2)
	vs.Record(2, 129025, ts, []byte{0x03}, 3) // unknown device

	snap := vs.Snapshot(reg, &EventFilter{
		PGNs:          []uint32{129025},
		Manufacturers: []string{"Garmin"},
	})
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if snap[0].Source != 1 {
		t.Errorf("expected source 1, got %d", snap[0].Source)
	}
	if len(snap[0].Values) != 1 {
		t.Fatalf("expected 1 PGN value, got %d", len(snap[0].Values))
	}
	if snap[0].Values[0].PGN != 129025 {
		t.Errorf("expected PGN 129025, got %d", snap[0].Values[0].PGN)
	}
}

func TestValueStoreDecodedSnapshot(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

	// PGN 129025 Position Rapid Update: lat=47.6062°, lon=-122.3321°
	posData := make([]byte, 8)
	binary.LittleEndian.PutUint32(posData[0:4], uint32(int32(476062000)))   // lat * 1e7
	binary.LittleEndian.PutUint32(posData[4:8], uint32(int32(-1223321000))) // lon * 1e7
	vs.Record(1, 129025, ts, posData, 10)

	snap := vs.DecodedSnapshot(reg, nil)
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if len(snap[0].Values) != 1 {
		t.Fatalf("expected 1 decoded value, got %d", len(snap[0].Values))
	}
	dv := snap[0].Values[0]
	if dv.PGN != 129025 {
		t.Errorf("pgn: got %d, want 129025", dv.PGN)
	}
	if dv.Description != "Position Rapid Update" {
		t.Errorf("description: got %q, want %q", dv.Description, "Position Rapid Update")
	}
	if dv.Seq != 10 {
		t.Errorf("seq: got %d, want 10", dv.Seq)
	}
	if dv.Fields == nil {
		t.Fatal("fields should not be nil")
	}

	// Verify JSON round-trip includes decoded fields.
	jsonBytes := vs.DecodedSnapshotJSON(reg, nil)
	var result []json.RawMessage
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 device in JSON, got %d", len(result))
	}
	jsonStr := string(jsonBytes)
	if !strings.Contains(jsonStr, "latitude") || !strings.Contains(jsonStr, "longitude") {
		t.Errorf("JSON should contain decoded field names, got: %s", jsonStr)
	}
}

func TestValueStoreDecodedSnapshotUnknownPGN(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	// Use a PGN unlikely to be in the registry.
	vs.Record(1, 999999, ts, []byte{0x01, 0x02}, 1)

	snap := vs.DecodedSnapshot(reg, nil)
	if len(snap) != 0 {
		t.Errorf("expected 0 device groups for unknown PGN, got %d", len(snap))
	}
}

func TestValueStoreDecodedSnapshotEmpty(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	jsonBytes := vs.DecodedSnapshotJSON(reg, nil)
	if string(jsonBytes) != "[]" {
		t.Errorf("empty decoded store should return [], got %s", string(jsonBytes))
	}
}

func TestValueStoreDecodedSnapshotWithFilter(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

	// Two valid PGNs.
	posData := make([]byte, 8)
	binary.LittleEndian.PutUint32(posData[0:4], uint32(int32(100000000)))
	binary.LittleEndian.PutUint32(posData[4:8], uint32(int32(-200000000)))
	vs.Record(1, 129025, ts, posData, 1)

	windData := make([]byte, 8)
	windData[0] = 0                                              // sid
	binary.LittleEndian.PutUint16(windData[1:3], 550)            // speed 5.5 m/s
	binary.LittleEndian.PutUint16(windData[3:5], 12345)          // angle
	windData[5] = 2                                              // apparent
	vs.Record(1, 130306, ts, windData, 2)

	snap := vs.DecodedSnapshot(reg, &EventFilter{PGNs: []uint32{129025}})
	if len(snap) != 1 {
		t.Fatalf("expected 1 device group, got %d", len(snap))
	}
	if len(snap[0].Values) != 1 {
		t.Fatalf("expected 1 value, got %d", len(snap[0].Values))
	}
	if snap[0].Values[0].PGN != 129025 {
		t.Errorf("expected PGN 129025, got %d", snap[0].Values[0].PGN)
	}
}

func TestValueStoreSnapshotFilterMatchesNothing(t *testing.T) {
	vs := NewValueStore()
	reg := NewDeviceRegistry()

	ts := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	vs.Record(1, 129025, ts, []byte{0x01}, 1)

	snap := vs.Snapshot(reg, &EventFilter{PGNs: []uint32{60928}})
	if len(snap) != 0 {
		t.Errorf("expected 0 device groups, got %d", len(snap))
	}
}
