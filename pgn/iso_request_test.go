package pgn

import (
	"encoding/hex"
	"testing"
)

func TestDecodeISORequest(t *testing.T) {
	// Real frame: requesting PGN 60928 (ISO Address Claim).
	raw, _ := hex.DecodeString("00ee00")
	m, err := DecodeISORequest(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.RequestedPgn != 60928 {
		t.Errorf("RequestedPgn = %d, want 60928", m.RequestedPgn)
	}
}

func TestDecodeISORequestShortPads(t *testing.T) {
	// 1 byte: padded with 0xFF, result is 0xFFFF00 = 16776960.
	m, err := DecodeISORequest([]byte{0x00})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := uint32(0x00FFFF00)
	if m.RequestedPgn != want {
		t.Errorf("RequestedPgn = %d, want %d", m.RequestedPgn, want)
	}
}

func TestDecodeISORequestEncode(t *testing.T) {
	m := ISORequest{RequestedPgn: 60928}
	data := m.Encode()
	m2, err := DecodeISORequest(data)
	if err != nil {
		t.Fatalf("decode roundtrip: %v", err)
	}
	if m2.RequestedPgn != 60928 {
		t.Errorf("roundtrip: got %d, want 60928", m2.RequestedPgn)
	}
}

func TestISORequestRegistry(t *testing.T) {
	info, ok := Registry[59904]
	if !ok {
		t.Fatal("PGN 59904 not in registry")
	}
	if info.Description != "ISO Request" {
		t.Errorf("description = %q", info.Description)
	}
}
