package pgn

import (
	"encoding/hex"
	"testing"
)

func TestDecodeGNSSSatsInView(t *testing.T) {
	// Real frame from a GPS receiver: 10 satellites in view.
	raw, _ := hex.DecodeString("69fd0a031622f275eb0fffffff7ff504dc351ee7480effffff7ff506ae0f59c4e40cffffff7ff509b920c2c7b20effffff7ff51a961a0a2fd70dffffff7ff51f00000000f70fffffff7ff1447f251640480fffffff7ff54e8a27452a1610ffffff7ff54f6821e6739f0effffff7ff554d10642b14709ffffff7ff5")

	m, err := DecodeGNSSSatsInView(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if m.SID != 0x69 {
		t.Errorf("SID = %d, want %d", m.SID, 0x69)
	}
	if m.RangeResidualMode != 1 {
		t.Errorf("RangeResidualMode = %d, want 1", m.RangeResidualMode)
	}
	if m.SatsInView != 10 {
		t.Errorf("SatsInView = %d, want 10", m.SatsInView)
	}
	if len(m.Satellites) != 10 {
		t.Fatalf("len(Satellites) = %d, want 10", len(m.Satellites))
	}

	// Spot-check first satellite.
	s0 := m.Satellites[0]
	if s0.PRN != 3 {
		t.Errorf("sat[0].PRN = %d, want 3", s0.PRN)
	}
	// Status should be a small value (0-5).
	if s0.Status > 5 {
		t.Errorf("sat[0].Status = %d, want <= 5", s0.Status)
	}
}

func TestDecodeGNSSSatsInViewShort(t *testing.T) {
	// Header says 3 sats but only enough data for 1 (3 + 12 = 15 bytes).
	raw, _ := hex.DecodeString("ff0103" + "0102030405060708090a0b0c")
	m, err := DecodeGNSSSatsInView(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.SatsInView != 3 {
		t.Errorf("SatsInView = %d, want 3", m.SatsInView)
	}
	// Should only decode 1 satellite (data for 1 available).
	if len(m.Satellites) != 1 {
		t.Errorf("len(Satellites) = %d, want 1", len(m.Satellites))
	}
}

func TestDecodeGNSSSatsInViewEmpty(t *testing.T) {
	// Zero sats in view.
	raw, _ := hex.DecodeString("ff0100")
	m, err := DecodeGNSSSatsInView(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(m.Satellites) != 0 {
		t.Errorf("len(Satellites) = %d, want 0", len(m.Satellites))
	}
}

func TestDecodeGNSSSatsInViewTooShort(t *testing.T) {
	_, err := DecodeGNSSSatsInView([]byte{0x00})
	if err == nil {
		t.Fatal("expected error for data shorter than header")
	}
}

func TestGNSSSatsInViewRegistry(t *testing.T) {
	info, ok := Registry[129540]
	if !ok {
		t.Fatal("PGN 129540 not in registry")
	}
	if info.Description != "GNSS Sats in View" {
		t.Errorf("description = %q", info.Description)
	}
}
