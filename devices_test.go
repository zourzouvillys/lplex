package lplex

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/sixfathoms/lplex/canbus"
)

func TestDecodeNAME(t *testing.T) {
	// Build a known NAME value:
	//   uniqueNumber     = 12345  (21 bits)
	//   manufacturerCode = 229    (11 bits) = Garmin
	//   instanceLower    = 0      (3 bits)
	//   instanceUpper    = 0      (5 bits)
	//   deviceFunction   = 150    (8 bits)
	//   reserved         = 0      (1 bit)
	//   deviceClass      = 40     (7 bits)
	//   systemInstance   = 0      (4 bits)
	//   industryGroup    = 4      (3 bits) = Marine
	//   arbitraryAddr    = 1      (1 bit)
	var name uint64
	name |= uint64(12345)                  // bits 0-20
	name |= uint64(229) << 21              // bits 21-31
	name |= uint64(0) << 32               // instanceLower bits 32-34
	name |= uint64(0) << 35               // instanceUpper bits 35-39
	name |= uint64(150) << 40             // deviceFunction bits 40-47
	name |= uint64(0) << 48              // reserved bit 48
	name |= uint64(40) << 49              // deviceClass bits 49-55
	name |= uint64(0) << 56              // systemInstance bits 56-59
	name |= uint64(4) << 60              // industryGroup bits 60-62
	name |= uint64(1) << 63              // arbitrary address capable

	dev := decodeNAME(name, 1)

	if dev.Source != 1 {
		t.Errorf("source: got %d, want 1", dev.Source)
	}
	if dev.UniqueNumber != 12345 {
		t.Errorf("uniqueNumber: got %d, want 12345", dev.UniqueNumber)
	}
	if dev.ManufacturerCode != 229 {
		t.Errorf("manufacturerCode: got %d, want 229", dev.ManufacturerCode)
	}
	if dev.Manufacturer != "Garmin" {
		t.Errorf("manufacturer: got %q, want %q", dev.Manufacturer, "Garmin")
	}
	if dev.DeviceClass != 40 {
		t.Errorf("deviceClass: got %d, want 40", dev.DeviceClass)
	}
	if dev.DeviceFunction != 150 {
		t.Errorf("deviceFunction: got %d, want 150", dev.DeviceFunction)
	}
	if dev.DeviceInstance != 0 {
		t.Errorf("deviceInstance: got %d, want 0", dev.DeviceInstance)
	}
}

func TestDecodeNAMEWithInstance(t *testing.T) {
	// deviceInstance = (upper << 3) | lower
	// lower = 5 (bits 32-34), upper = 2 (bits 35-39) => instance = (2<<3)|5 = 21
	var name uint64
	name |= uint64(99999)
	name |= uint64(135) << 21  // Airmar
	name |= uint64(5) << 32   // instanceLower
	name |= uint64(2) << 35   // instanceUpper
	name |= uint64(130) << 40 // deviceFunction
	name |= uint64(75) << 49  // deviceClass

	dev := decodeNAME(name, 42)

	if dev.DeviceInstance != 21 {
		t.Errorf("deviceInstance: got %d, want 21", dev.DeviceInstance)
	}
	if dev.Manufacturer != "Airmar" {
		t.Errorf("manufacturer: got %q, want %q", dev.Manufacturer, "Airmar")
	}
}

func TestDeviceRegistryNewDevice(t *testing.T) {
	reg := NewDeviceRegistry()

	data := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21 // Garmin
	binary.LittleEndian.PutUint64(data, name)

	dev, _, _ := reg.HandleAddressClaim(1, data)
	if dev == nil {
		t.Fatal("expected new device")
	}
	if dev.Manufacturer != "Garmin" {
		t.Errorf("manufacturer: got %q, want Garmin", dev.Manufacturer)
	}
}

func TestDeviceRegistryDuplicate(t *testing.T) {
	reg := NewDeviceRegistry()

	data := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21
	binary.LittleEndian.PutUint64(data, name)

	reg.HandleAddressClaim(1, data)

	// Same NAME, same source: no change
	dev, _, _ := reg.HandleAddressClaim(1, data)
	if dev != nil {
		t.Error("expected nil for duplicate address claim")
	}
}

func TestDeviceRegistryChanged(t *testing.T) {
	reg := NewDeviceRegistry()

	data1 := make([]byte, 8)
	binary.LittleEndian.PutUint64(data1, uint64(229)<<21)
	reg.HandleAddressClaim(1, data1)

	// Different NAME on same source: address change
	data2 := make([]byte, 8)
	binary.LittleEndian.PutUint64(data2, uint64(135)<<21)
	dev, _, _ := reg.HandleAddressClaim(1, data2)
	if dev == nil {
		t.Fatal("expected new device for changed NAME")
	}
	if dev.Manufacturer != "Airmar" {
		t.Errorf("manufacturer: got %q, want Airmar", dev.Manufacturer)
	}
}

func TestDeviceRegistrySnapshot(t *testing.T) {
	reg := NewDeviceRegistry()

	for _, src := range []uint8{1, 5, 10} {
		data := make([]byte, 8)
		binary.LittleEndian.PutUint64(data, uint64(229)<<21|uint64(src))
		reg.HandleAddressClaim(src, data)
	}

	snap := reg.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(snap))
	}
}

func TestDeviceRegistryShortData(t *testing.T) {
	reg := NewDeviceRegistry()
	dev, _, _ := reg.HandleAddressClaim(1, []byte{0x01, 0x02})
	if dev != nil {
		t.Error("expected nil for short data")
	}
}

func TestLookupUnknownManufacturer(t *testing.T) {
	name := canbus.LookupManufacturer(9999)
	if name != "Unknown (9999)" {
		t.Errorf("got %q, want 'Unknown (9999)'", name)
	}
}

func TestRecordPacketIgnoresReservedAddresses(t *testing.T) {
	reg := NewDeviceRegistry()
	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	for _, src := range []uint8{254, 255} {
		isNew := reg.RecordPacket(src, ts, 8)
		if isNew {
			t.Errorf("src=%d: expected false, got true", src)
		}
	}

	if snap := reg.Snapshot(); len(snap) != 0 {
		t.Errorf("expected 0 devices, got %d", len(snap))
	}
}

func TestRecordPacketNewSource(t *testing.T) {
	reg := NewDeviceRegistry()
	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	isNew := reg.RecordPacket(42, ts, 8)
	if !isNew {
		t.Error("expected true for new source")
	}

	snap := reg.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 device, got %d", len(snap))
	}
	dev := snap[0]
	if dev.Source != 42 {
		t.Errorf("source: got %d, want 42", dev.Source)
	}
	if dev.PacketCount != 1 {
		t.Errorf("packet count: got %d, want 1", dev.PacketCount)
	}
	if dev.ByteCount != 8 {
		t.Errorf("byte count: got %d, want 8", dev.ByteCount)
	}
	if !dev.FirstSeen.Equal(ts) {
		t.Errorf("first_seen: got %v, want %v", dev.FirstSeen, ts)
	}
	if !dev.LastSeen.Equal(ts) {
		t.Errorf("last_seen: got %v, want %v", dev.LastSeen, ts)
	}
	// Stats-only entry: identity fields should be zero values.
	if dev.Manufacturer != "" {
		t.Errorf("manufacturer should be empty for stats-only entry, got %q", dev.Manufacturer)
	}
}

func TestRecordPacketIncrementsExisting(t *testing.T) {
	reg := NewDeviceRegistry()
	t0 := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)

	reg.RecordPacket(7, t0, 8)

	isNew := reg.RecordPacket(7, t1, 16)
	if isNew {
		t.Error("expected false for existing source")
	}

	snap := reg.Snapshot()
	dev := snap[0]
	if dev.PacketCount != 2 {
		t.Errorf("packet count: got %d, want 2", dev.PacketCount)
	}
	if dev.ByteCount != 24 {
		t.Errorf("byte count: got %d, want 24", dev.ByteCount)
	}
	if !dev.FirstSeen.Equal(t0) {
		t.Errorf("first_seen should not change: got %v, want %v", dev.FirstSeen, t0)
	}
	if !dev.LastSeen.Equal(t1) {
		t.Errorf("last_seen: got %v, want %v", dev.LastSeen, t1)
	}
}

func TestHandleAddressClaimPreservesStats(t *testing.T) {
	reg := NewDeviceRegistry()
	t0 := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Second)

	// Record some packets first.
	reg.RecordPacket(1, t0, 8)
	reg.RecordPacket(1, t1, 8)

	// Now an address claim arrives for the same source.
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, uint64(229)<<21) // Garmin
	dev, _, _ := reg.HandleAddressClaim(1, data)

	if dev == nil {
		t.Fatal("expected new device from address claim")
	}
	if dev.Manufacturer != "Garmin" {
		t.Errorf("manufacturer: got %q, want Garmin", dev.Manufacturer)
	}
	if dev.PacketCount != 2 {
		t.Errorf("packet count should be preserved: got %d, want 2", dev.PacketCount)
	}
	if !dev.FirstSeen.Equal(t0) {
		t.Errorf("first_seen should be preserved: got %v, want %v", dev.FirstSeen, t0)
	}
	if !dev.LastSeen.Equal(t1) {
		t.Errorf("last_seen should be preserved: got %v, want %v", dev.LastSeen, t1)
	}
}

// buildProductInfoPayload builds a 134-byte PGN 126996 payload with the given fields.
func buildProductInfoPayload(productCode uint16, modelID, swVersion, modelVersion, modelSerial string) []byte {
	data := make([]byte, 134)
	// bytes 0-1: NMEA 2000 version (don't care for our decode)
	// bytes 2-3: product code
	binary.LittleEndian.PutUint16(data[0:2], 0x0834) // version 2.1 (arbitrary)
	binary.LittleEndian.PutUint16(data[2:4], productCode)
	copy(data[4:36], modelID)
	copy(data[36:76], swVersion)
	copy(data[76:100], modelVersion)
	copy(data[100:132], modelSerial)
	data[132] = 0x01 // certification level
	data[133] = 0x01 // load equivalency
	return data
}

func TestHandleProductInfoDecodesFields(t *testing.T) {
	reg := NewDeviceRegistry()

	// Device must exist first (from address claim flow).
	claim := make([]byte, 8)
	binary.LittleEndian.PutUint64(claim, uint64(229)<<21) // Garmin
	reg.HandleAddressClaim(1, claim)

	payload := buildProductInfoPayload(4242, "GPS 19x", "4.80", "1.0", "12345678")

	dev := reg.HandleProductInfo(1, payload)
	if dev == nil {
		t.Fatal("expected device update from product info")
	}
	if dev.ProductCode != 4242 {
		t.Errorf("product_code: got %d, want 4242", dev.ProductCode)
	}
	if dev.ModelID != "GPS 19x" {
		t.Errorf("model_id: got %q, want %q", dev.ModelID, "GPS 19x")
	}
	if dev.SoftwareVersion != "4.80" {
		t.Errorf("software_version: got %q, want %q", dev.SoftwareVersion, "4.80")
	}
	if dev.ModelVersion != "1.0" {
		t.Errorf("model_version: got %q, want %q", dev.ModelVersion, "1.0")
	}
	if dev.ModelSerial != "12345678" {
		t.Errorf("model_serial: got %q, want %q", dev.ModelSerial, "12345678")
	}
	// Identity fields from address claim should still be present.
	if dev.Manufacturer != "Garmin" {
		t.Errorf("manufacturer should be preserved: got %q", dev.Manufacturer)
	}
}

func TestHandleProductInfoMergesExisting(t *testing.T) {
	reg := NewDeviceRegistry()
	t0 := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Build up stats + identity.
	reg.RecordPacket(5, t0, 100)
	claim := make([]byte, 8)
	binary.LittleEndian.PutUint64(claim, uint64(591)<<21) // Raymarine
	reg.HandleAddressClaim(5, claim)

	payload := buildProductInfoPayload(999, "Axiom Pro", "3.10", "2.0", "SERIAL")
	dev := reg.HandleProductInfo(5, payload)

	if dev == nil {
		t.Fatal("expected device from product info")
	}
	if dev.Manufacturer != "Raymarine" {
		t.Errorf("manufacturer: got %q, want Raymarine", dev.Manufacturer)
	}
	if dev.PacketCount != 1 {
		t.Errorf("packet count should be preserved: got %d, want 1", dev.PacketCount)
	}
	if dev.ByteCount != 100 {
		t.Errorf("byte count should be preserved: got %d, want 100", dev.ByteCount)
	}
	if dev.ModelID != "Axiom Pro" {
		t.Errorf("model_id: got %q, want Axiom Pro", dev.ModelID)
	}
}

func TestHandleProductInfoDuplicateNoChange(t *testing.T) {
	reg := NewDeviceRegistry()
	claim := make([]byte, 8)
	binary.LittleEndian.PutUint64(claim, uint64(229)<<21)
	reg.HandleAddressClaim(1, claim)

	payload := buildProductInfoPayload(100, "Test", "1.0", "1.0", "SN1")
	reg.HandleProductInfo(1, payload)

	// Same payload again should return nil (no change).
	dev := reg.HandleProductInfo(1, payload)
	if dev != nil {
		t.Error("expected nil for duplicate product info")
	}
}

func TestHandleProductInfoUnknownSource(t *testing.T) {
	reg := NewDeviceRegistry()
	payload := buildProductInfoPayload(100, "Test", "1.0", "1.0", "SN1")

	dev := reg.HandleProductInfo(99, payload)
	if dev != nil {
		t.Error("expected nil for unknown source")
	}
}

func TestHandleProductInfoShortData(t *testing.T) {
	reg := NewDeviceRegistry()
	claim := make([]byte, 8)
	binary.LittleEndian.PutUint64(claim, uint64(229)<<21)
	reg.HandleAddressClaim(1, claim)

	dev := reg.HandleProductInfo(1, make([]byte, 100)) // too short
	if dev != nil {
		t.Error("expected nil for short data")
	}
}

func TestSynthesizeFramesRoundtrip(t *testing.T) {
	src := NewDeviceRegistry()

	// Device 1: address claim + product info (Garmin GPS)
	claim1 := make([]byte, 8)
	var name1 uint64
	name1 |= uint64(12345)      // unique number
	name1 |= uint64(229) << 21  // Garmin
	name1 |= uint64(150) << 40  // device function
	name1 |= uint64(40) << 49   // device class
	name1 |= uint64(4) << 60    // marine
	name1 |= uint64(1) << 63    // arbitrary address
	binary.LittleEndian.PutUint64(claim1, name1)
	src.HandleAddressClaim(1, claim1)
	src.HandleProductInfo(1, buildProductInfoPayload(4242, "GPS 19x", "4.80", "1.0", "SN-001"))

	// Device 2: address claim only, no product info (Airmar)
	claim2 := make([]byte, 8)
	var name2 uint64
	name2 |= uint64(99999)      // unique number
	name2 |= uint64(135) << 21  // Airmar
	name2 |= uint64(130) << 40  // device function
	name2 |= uint64(75) << 49   // device class
	binary.LittleEndian.PutUint64(claim2, name2)
	src.HandleAddressClaim(5, claim2)

	// Device 3: stats-only entry (no NAME), should be skipped.
	src.RecordPacket(99, time.Now(), 8)

	ts := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	frames := src.SynthesizeFrames(ts)

	// Device 1 => 2 frames (claim + product), Device 2 => 1 frame (claim only).
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}

	// Feed all synthesized frames into a fresh registry.
	dst := NewDeviceRegistry()
	for _, f := range frames {
		switch f.Header.PGN {
		case 60928:
			dst.HandleAddressClaim(f.Header.Source, f.Data)
		case 126996:
			dst.HandleProductInfo(f.Header.Source, f.Data)
		}
	}

	// Verify device 1 roundtripped correctly.
	d1 := dst.Get(1)
	if d1 == nil {
		t.Fatal("device 1 missing")
	}
	if d1.NAME != name1 {
		t.Errorf("device 1 NAME: got %x, want %x", d1.NAME, name1)
	}
	if d1.Manufacturer != "Garmin" {
		t.Errorf("device 1 manufacturer: got %q, want Garmin", d1.Manufacturer)
	}
	if d1.ProductCode != 4242 {
		t.Errorf("device 1 product code: got %d, want 4242", d1.ProductCode)
	}
	if d1.ModelID != "GPS 19x" {
		t.Errorf("device 1 model_id: got %q, want %q", d1.ModelID, "GPS 19x")
	}
	if d1.SoftwareVersion != "4.80" {
		t.Errorf("device 1 software_version: got %q, want %q", d1.SoftwareVersion, "4.80")
	}
	if d1.ModelVersion != "1.0" {
		t.Errorf("device 1 model_version: got %q, want %q", d1.ModelVersion, "1.0")
	}
	if d1.ModelSerial != "SN-001" {
		t.Errorf("device 1 model_serial: got %q, want %q", d1.ModelSerial, "SN-001")
	}

	// Verify device 2 roundtripped correctly (claim only, no product info).
	d2 := dst.Get(5)
	if d2 == nil {
		t.Fatal("device 5 missing")
	}
	if d2.NAME != name2 {
		t.Errorf("device 5 NAME: got %x, want %x", d2.NAME, name2)
	}
	if d2.Manufacturer != "Airmar" {
		t.Errorf("device 5 manufacturer: got %q, want Airmar", d2.Manufacturer)
	}
	if d2.ProductCode != 0 {
		t.Errorf("device 5 should have no product code, got %d", d2.ProductCode)
	}

	// Stats-only device (src 99) should not appear.
	if dst.Get(99) != nil {
		t.Error("stats-only device (src 99) should not be synthesized")
	}
}

func TestSynthesizeFramesEmpty(t *testing.T) {
	reg := NewDeviceRegistry()
	frames := reg.SynthesizeFrames(time.Now())
	if len(frames) != 0 {
		t.Errorf("expected 0 frames from empty registry, got %d", len(frames))
	}
}

func TestDecodeFixedString(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"normal", []byte("hello\x00\x00\x00"), "hello"},
		{"ff padding", []byte("test\xff\xff\xff"), "test"},
		{"mixed padding", []byte("abc\x00XY\xff"), "abc"},
		{"all nulls", []byte{0, 0, 0, 0}, ""},
		{"all 0xFF", []byte{0xFF, 0xFF, 0xFF}, ""},
		{"no padding", []byte("full"), "full"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeFixedString(tt.input)
			if got != tt.want {
				t.Errorf("decodeFixedString(%v): got %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHandleAddressClaimEvictsSameNAME(t *testing.T) {
	reg := NewDeviceRegistry()

	// Device with NAME X claims source 5.
	nameX := uint64(229)<<21 | uint64(42)
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, nameX)
	reg.HandleAddressClaim(5, data)

	// Same NAME now claims source 10 (device restarted, new address).
	dev, evictedSrc, evicted := reg.HandleAddressClaim(10, data)
	if dev == nil {
		t.Fatal("expected new device")
	}
	if !evicted {
		t.Fatal("expected eviction of old source")
	}
	if evictedSrc != 5 {
		t.Errorf("evicted source: got %d, want 5", evictedSrc)
	}
	if dev.Source != 10 {
		t.Errorf("new device source: got %d, want 10", dev.Source)
	}

	// Old source should be gone.
	if reg.Get(5) != nil {
		t.Error("old source 5 should have been evicted")
	}
	// New source should be present.
	if reg.Get(10) == nil {
		t.Error("new source 10 should exist")
	}
}

func TestHandleAddressClaimNoEvictionSameSource(t *testing.T) {
	reg := NewDeviceRegistry()

	nameX := uint64(229)<<21 | uint64(42)
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, nameX)
	reg.HandleAddressClaim(5, data)

	// Different NAME on the same source is not an eviction, just a change.
	nameY := uint64(135)<<21 | uint64(99)
	data2 := make([]byte, 8)
	binary.LittleEndian.PutUint64(data2, nameY)
	dev, _, evicted := reg.HandleAddressClaim(5, data2)
	if dev == nil {
		t.Fatal("expected new device for changed NAME")
	}
	if evicted {
		t.Error("should not evict when source address hasn't changed")
	}
}

func TestExpireIdle(t *testing.T) {
	reg := NewDeviceRegistry()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Source 1: seen recently.
	reg.RecordPacket(1, base, 8)

	// Source 2: seen a long time ago.
	reg.RecordPacket(2, base.Add(-10*time.Minute), 8)

	// Source 3: also stale.
	reg.RecordPacket(3, base.Add(-10*time.Minute), 8)

	// Source 4: with an address claim, but stale.
	reg.RecordPacket(4, base.Add(-10*time.Minute), 8)
	claim := make([]byte, 8)
	binary.LittleEndian.PutUint64(claim, uint64(229)<<21)
	reg.HandleAddressClaim(4, claim)

	cutoff := base.Add(-5 * time.Minute)
	evicted := reg.ExpireIdle(cutoff)

	// Sources 2, 3, 4 should be evicted (LastSeen before cutoff).
	if len(evicted) != 3 {
		t.Fatalf("expected 3 evictions, got %d: %v", len(evicted), evicted)
	}

	// Source 1 should survive.
	if reg.Get(1) == nil {
		t.Error("source 1 should not be expired")
	}

	// The evicted ones should be gone.
	for _, src := range []uint8{2, 3, 4} {
		if reg.Get(src) != nil {
			t.Errorf("source %d should have been expired", src)
		}
	}
}

func TestExpireIdleNothingToExpire(t *testing.T) {
	reg := NewDeviceRegistry()
	now := time.Now()
	reg.RecordPacket(1, now, 8)

	evicted := reg.ExpireIdle(now.Add(-time.Hour))
	if len(evicted) != 0 {
		t.Errorf("expected no evictions, got %d", len(evicted))
	}
}
