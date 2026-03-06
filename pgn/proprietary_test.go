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
	if m.RegisterId != 0x0FFF {
		t.Errorf("RegisterId = 0x%04X, want 0x0FFF", m.RegisterId)
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
	if m.RegisterId != 0xED8F {
		t.Errorf("RegisterId = 0x%04X, want 0xED8F", m.RegisterId)
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
	if m.RegisterId != 0x0200 {
		t.Errorf("RegisterId = 0x%04X, want 0x0200", m.RegisterId)
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
	if m.RegisterId != 0xEEFF {
		t.Errorf("RegisterId = 0x%04X, want 0xEEFF", m.RegisterId)
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
	if m.RegisterId != 0x0301 {
		t.Errorf("RegisterId = 0x%04X, want 0x0301", m.RegisterId)
	}
	if m.RegisterName() != "" {
		t.Errorf("RegisterName() = %q, want empty", m.RegisterName())
	}
}

func TestDecodeProprietaryUnknownManufacturer(t *testing.T) {
	// Synthetic: manufacturer 123 (0x7B), industry 4.
	// Packed: 0x007B | (0x03 << 11) | (0x04 << 13) = 0x987B, LE bytes: 7B 98.
	raw, _ := hex.DecodeString("7b98aabbccddeeff")
	result, err := Registry[61184].Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, ok := result.(ProprietarySingleFrame)
	if !ok {
		t.Fatalf("expected ProprietarySingleFrame, got %T", result)
	}
	if m.ManufacturerCode != 123 {
		t.Errorf("ManufacturerCode = %d, want 123", m.ManufacturerCode)
	}
	if m.IndustryCode != 4 {
		t.Errorf("IndustryCode = %d, want 4", m.IndustryCode)
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
		RegisterId:   0x0FFF,
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
	if got.RegisterId != 0x0FFF {
		t.Errorf("RegisterId = 0x%04X, want 0x0FFF", got.RegisterId)
	}
	if got.Payload != 10000 {
		t.Errorf("Payload = %d, want 10000", got.Payload)
	}
}
