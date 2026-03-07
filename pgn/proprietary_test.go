package pgn

import (
	"encoding/hex"
	"testing"
)

func TestDecodeVictronSOC(t *testing.T) {
	// Real frame: SOC register (0x0FFF) = 10000 (100.00% at 0.01 scale).
	raw, _ := hex.DecodeString("6699ff0f10270000")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	if m.ManufacturerCode != 358 {
		t.Errorf("ManufacturerCode = %d, want 358", m.ManufacturerCode)
	}
	if m.IndustryCode != 4 {
		t.Errorf("IndustryCode = %d, want 4", m.IndustryCode)
	}
	if m.Register != 0x0FFF {
		t.Errorf("RegisterId = 0x%04X, want 0x0FFF", m.Register)
	}
	if m.RegisterName() != "State of Charge" {
		t.Errorf("RegisterName() = %q, want %q", m.RegisterName(), "State of Charge")
	}
	if m.Payload != 10000 {
		t.Errorf("Payload = %d, want 10000", m.Payload)
	}
}

func TestDecodeVictronCurrent(t *testing.T) {
	// Real frame: DC Channel 1 Current (0xED8F) = 33 (3.3A at 0.1 scale).
	raw, _ := hex.DecodeString("66998fed21000000")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	if m.Register != 0xED8F {
		t.Errorf("RegisterId = 0x%04X, want 0xED8F", m.Register)
	}
	if m.RegisterName() != "DC Channel 1 Current" {
		t.Errorf("RegisterName() = %q, want %q", m.RegisterName(), "DC Channel 1 Current")
	}
	if m.Payload != 33 {
		t.Errorf("Payload = %d, want 33", m.Payload)
	}
}

func TestDecodeVictronDeviceMode(t *testing.T) {
	// Real frame: Device Mode (0x0200) = 3.
	raw, _ := hex.DecodeString("6699000203000000")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	if m.Register != 0x0200 {
		t.Errorf("RegisterId = 0x%04X, want 0x0200", m.Register)
	}
	if m.RegisterName() != "Device Mode" {
		t.Errorf("RegisterName() = %q, want %q", m.RegisterName(), "Device Mode")
	}
	if m.Payload != 3 {
		t.Errorf("Payload = %d, want 3", m.Payload)
	}
}

func TestDecodeVictronDischargeSinceFull(t *testing.T) {
	// Real frame: Discharge Since Full (0xEEFF) = 0.
	raw, _ := hex.DecodeString("6699ffee00000000")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	if m.Register != 0xEEFF {
		t.Errorf("RegisterId = 0x%04X, want 0xEEFF", m.Register)
	}
	if m.RegisterName() != "Discharge Since Full" {
		t.Errorf("RegisterName() = %q, want %q", m.RegisterName(), "Discharge Since Full")
	}
}

func TestDecodeVictronUnknownRegister(t *testing.T) {
	// Real frame: unknown register 0x0301, value 0.
	raw, _ := hex.DecodeString("6699010300000000")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	if m.Register != 0x0301 {
		t.Errorf("RegisterId = 0x%04X, want 0x0301", m.Register)
	}
	if m.RegisterName() != "" {
		t.Errorf("RegisterName() = %q, want empty", m.RegisterName())
	}
}

func TestDecodeProprietaryUnknownManufacturer(t *testing.T) {
	// Unknown manufacturer codes should return (nil, nil), not an error.
	// We just can't decode this manufacturer's proprietary format.
	raw, _ := hex.DecodeString("7b98aabbccddeeff")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("unexpected error for unknown manufacturer code: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for unknown manufacturer code, got %v", result)
	}
}

func TestProprietaryRegistry(t *testing.T) {
	info, ok := Registry[61184]
	if !ok {
		t.Fatal("PGN 61184 not in registry")
	}
	if info.Description != "Proprietary Single Frame" {
		t.Errorf("description = %q", info.Description)
	}
}

func TestVictronBatteryRegisterEncode(t *testing.T) {
	m := VictronBatteryRegister{
		IndustryCode: 4,
		Register:   0x0FFF,
		Payload:      10000,
	}
	data := m.Encode()
	// Decode it back via the dispatch function to verify round-trip.
	result, err := Registry[61184].Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	// ManufacturerCode should be 358 (hardcoded by constrained encode).
	if got.ManufacturerCode != 358 {
		t.Errorf("ManufacturerCode = %d, want 358", got.ManufacturerCode)
	}
	if got.Register != 0x0FFF {
		t.Errorf("RegisterId = 0x%04X, want 0x0FFF", got.Register)
	}
	if got.Payload != 10000 {
		t.Errorf("Payload = %d, want 10000", got.Payload)
	}
}

func TestVictronEncodeIgnoresManufacturerCodeField(t *testing.T) {
	// Encode should hardcode manufacturer_code=358 regardless of the struct value.
	m := VictronBatteryRegister{
		ManufacturerCode: 999, // garbage, should be ignored
		IndustryCode:     4,
		Register:       0x0200,
		Payload:          42,
	}
	data := m.Encode()
	got, err := DecodeVictronBatteryRegister(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ManufacturerCode != 358 {
		t.Errorf("ManufacturerCode = %d, want 358 (hardcoded)", got.ManufacturerCode)
	}
	if got.Payload != 42 {
		t.Errorf("Payload = %d, want 42", got.Payload)
	}
}

func TestDispatchTooShort(t *testing.T) {
	// Decode61184 needs at least 2 bytes to read the discriminator.
	_, err := Decode61184([]byte{0x66})
	if err == nil {
		t.Fatal("expected error for 1-byte input")
	}
	_, err = Decode61184(nil)
	if err == nil {
		t.Fatal("expected error for nil input")
	}
}

func TestVictronDecodeShortData(t *testing.T) {
	// Only the 2-byte manufacturer header (Victron). Missing register_id and payload
	// should pad with 0xFF.
	raw, _ := hex.DecodeString("6699")
	result, err := Decode61184(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(VictronBatteryRegister)
	if !ok {
		t.Fatalf("expected VictronBatteryRegister, got %T", result)
	}
	if m.ManufacturerCode != 358 {
		t.Errorf("ManufacturerCode = %d, want 358", m.ManufacturerCode)
	}
	// Padded with 0xFF: register_id = 0xFFFF, payload = 0xFFFFFFFF.
	if m.Register != 0xFFFF {
		t.Errorf("RegisterId = 0x%04X, want 0xFFFF (padded)", m.Register)
	}
	if m.Payload != 0xFFFFFFFF {
		t.Errorf("Payload = 0x%08X, want 0xFFFFFFFF (padded)", m.Payload)
	}
}

func TestVictronDecodeEmpty(t *testing.T) {
	// DecodeVictronBatteryRegister directly with empty data should pad gracefully.
	m, err := DecodeVictronBatteryRegister(nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// All 0xFF padding: manufacturer_code = 0x07FF (11 bits of 0xFF).
	if m.ManufacturerCode != 0x07FF {
		t.Errorf("ManufacturerCode = %d, want %d (all-ones)", m.ManufacturerCode, 0x07FF)
	}
	if m.Register != 0xFFFF {
		t.Errorf("RegisterId = 0x%04X, want 0xFFFF", m.Register)
	}
	if m.Payload != 0xFFFFFFFF {
		t.Errorf("Payload = 0x%08X, want 0xFFFFFFFF", m.Payload)
	}
}

func TestVictronPGNMethod(t *testing.T) {
	if (VictronBatteryRegister{}).PGN() != 61184 {
		t.Error("VictronBatteryRegister.PGN() should be 61184")
	}
}
