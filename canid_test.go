package lplex

import "testing"

func TestParseCANID(t *testing.T) {
	tests := []struct {
		name     string
		id       uint32
		priority uint8
		pgn      uint32
		source   uint8
		dest     uint8
	}{
		{
			name:     "PGN 129025 Position Rapid from source 35",
			id:       0x09F80123,
			priority: 2,
			pgn:      129025,
			source:   0x23,
			dest:     0xFF,
		},
		{
			name:     "PDU2 broadcast with DP=1",
			id:       0x0DF11923,
			priority: 3,
			pgn:      0x1F119,
			source:   0x23,
			dest:     0xFF,
		},
		{
			name:     "PDU1 ISO Request broadcast",
			id:       0x18EAFF00,
			priority: 6,
			pgn:      0xEA00,
			source:   0x00,
			dest:     0xFF,
		},
		{
			name:     "PDU1 addressed message",
			id:       0x18EA0501,
			priority: 6,
			pgn:      0xEA00,
			source:   0x01,
			dest:     0x05,
		},
		{
			name:     "PGN 60928 ISO Address Claim",
			id:       0x18EEFF01,
			priority: 6,
			pgn:      0xEE00,
			source:   0x01,
			dest:     0xFF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := ParseCANID(tt.id)
			if h.Priority != tt.priority {
				t.Errorf("priority: got %d, want %d", h.Priority, tt.priority)
			}
			if h.PGN != tt.pgn {
				t.Errorf("PGN: got 0x%X (%d), want 0x%X (%d)", h.PGN, h.PGN, tt.pgn, tt.pgn)
			}
			if h.Source != tt.source {
				t.Errorf("source: got %d, want %d", h.Source, tt.source)
			}
			if h.Destination != tt.dest {
				t.Errorf("dest: got %d, want %d", h.Destination, tt.dest)
			}
		})
	}
}

func TestBuildCANID(t *testing.T) {
	tests := []struct {
		name   string
		header CANHeader
		want   uint32
	}{
		{
			name:   "PDU2 broadcast",
			header: CANHeader{Priority: 2, PGN: 129025, Source: 0x23, Destination: 0xFF},
			want:   0x09F80123,
		},
		{
			name:   "PDU1 addressed",
			header: CANHeader{Priority: 6, PGN: 0xEA00, Source: 0x01, Destination: 0x05},
			want:   0x18EA0501,
		},
		{
			name:   "PDU1 broadcast dest",
			header: CANHeader{Priority: 6, PGN: 0xEA00, Source: 0x00, Destination: 0xFF},
			want:   0x18EAFF00,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildCANID(tt.header)
			if got != tt.want {
				t.Errorf("BuildCANID: got 0x%08X, want 0x%08X", got, tt.want)
			}
		})
	}
}

func TestCANIDRoundTrip(t *testing.T) {
	// BuildCANID(ParseCANID(x)) == x for valid 29-bit CAN IDs
	ids := []uint32{
		0x09F80123, // PDU2 broadcast
		0x0DF11923, // PDU2 with DP=1
		0x18EAFF00, // PDU1 broadcast
		0x18EA0501, // PDU1 addressed
		0x18EEFF01, // ISO Address Claim
		0x09F1022B, // COG/SOG
		0x09FD0232, // Wind
		0x09F10D32, // Engine rapid
	}

	for _, id := range ids {
		h := ParseCANID(id)
		rebuilt := BuildCANID(h)
		if rebuilt != id {
			t.Errorf("round-trip failed for 0x%08X: ParseCANID gave PGN=0x%X src=%d dst=%d prio=%d, BuildCANID gave 0x%08X",
				id, h.PGN, h.Source, h.Destination, h.Priority, rebuilt)
		}
	}
}
