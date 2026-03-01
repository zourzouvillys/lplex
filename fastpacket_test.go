package lplex

import (
	"testing"
	"time"
)

func TestFastPacketAssembly(t *testing.T) {
	a := NewFastPacketAssembler(750 * time.Millisecond)
	now := time.Now()

	pgn := uint32(126996)
	src := uint8(35)

	// 20-byte fast-packet transfer, seq counter = 1
	frame0 := []byte{0x20, 20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	if result := a.Process(pgn, src, frame0, now); result != nil {
		t.Fatal("expected nil after frame 0")
	}

	frame1 := []byte{0x21, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D}
	if result := a.Process(pgn, src, frame1, now); result != nil {
		t.Fatal("expected nil after frame 1")
	}

	frame2 := []byte{0x22, 0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14}
	result := a.Process(pgn, src, frame2, now)
	if result == nil {
		t.Fatal("expected complete data after frame 2")
	}

	if len(result) != 20 {
		t.Fatalf("expected 20 bytes, got %d", len(result))
	}

	expected := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
		0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D,
		0x0E, 0x0F, 0x10, 0x11, 0x12, 0x13, 0x14,
	}
	for i := range expected {
		if result[i] != expected[i] {
			t.Errorf("byte %d: got 0x%02X, want 0x%02X", i, result[i], expected[i])
		}
	}
}

func TestFastPacketTimeout(t *testing.T) {
	a := NewFastPacketAssembler(100 * time.Millisecond)
	now := time.Now()

	frame0 := []byte{0x20, 20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	a.Process(126996, 35, frame0, now)

	frame1 := []byte{0x21, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D}
	if result := a.Process(126996, 35, frame1, now.Add(200*time.Millisecond)); result != nil {
		t.Fatal("expected nil due to timeout")
	}
}

func TestFastPacketOutOfOrder(t *testing.T) {
	a := NewFastPacketAssembler(750 * time.Millisecond)
	now := time.Now()

	frame0 := []byte{0x20, 20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	a.Process(126996, 35, frame0, now)

	// Skip frame 1, send frame 2
	frame2 := []byte{0x22, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D}
	if result := a.Process(126996, 35, frame2, now); result != nil {
		t.Fatal("expected nil for out-of-order frame")
	}
}

func TestFastPacketSeqMismatch(t *testing.T) {
	a := NewFastPacketAssembler(750 * time.Millisecond)
	now := time.Now()

	// Seq counter = 1
	frame0 := []byte{0x20, 20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	a.Process(126996, 35, frame0, now)

	// Frame 1 but with seq counter = 2 (different transfer)
	frame1 := []byte{0x41, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D}
	if result := a.Process(126996, 35, frame1, now); result != nil {
		t.Fatal("expected nil for seq mismatch")
	}
}

func TestFragmentFastPacket(t *testing.T) {
	// 20 bytes: frame 0 carries 6, frame 1 carries 7, frame 2 carries 7
	data := make([]byte, 20)
	for i := range data {
		data[i] = byte(i + 1)
	}

	frames := FragmentFastPacket(data, 3)

	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}

	// Frame 0: seq=3(011)<<5 | frame=0 = 0x60, then total len = 20
	if frames[0][0] != 0x60 {
		t.Errorf("frame 0 byte 0: got 0x%02X, want 0x60", frames[0][0])
	}
	if frames[0][1] != 20 {
		t.Errorf("frame 0 byte 1 (total len): got %d, want 20", frames[0][1])
	}
	for i := 0; i < 6; i++ {
		if frames[0][2+i] != byte(i+1) {
			t.Errorf("frame 0 data[%d]: got 0x%02X, want 0x%02X", i, frames[0][2+i], byte(i+1))
		}
	}

	// Frame 1: seq=3<<5 | frame=1 = 0x61
	if frames[1][0] != 0x61 {
		t.Errorf("frame 1 byte 0: got 0x%02X, want 0x61", frames[1][0])
	}
	for i := 0; i < 7; i++ {
		if frames[1][1+i] != byte(7+i) {
			t.Errorf("frame 1 data[%d]: got 0x%02X, want 0x%02X", i, frames[1][1+i], byte(7+i))
		}
	}

	// Frame 2: seq=3<<5 | frame=2 = 0x62
	if frames[2][0] != 0x62 {
		t.Errorf("frame 2 byte 0: got 0x%02X, want 0x62", frames[2][0])
	}
	for i := 0; i < 7; i++ {
		if frames[2][1+i] != byte(14+i) {
			t.Errorf("frame 2 data[%d]: got 0x%02X, want 0x%02X", i, frames[2][1+i], byte(14+i))
		}
	}
}

func TestFragmentFastPacketSmall(t *testing.T) {
	// 6 bytes fits entirely in frame 0
	data := []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}
	frames := FragmentFastPacket(data, 0)
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame for 6-byte payload, got %d", len(frames))
	}
	if frames[0][1] != 6 {
		t.Errorf("total length byte: got %d, want 6", frames[0][1])
	}
}

func TestFastPacketRoundTrip(t *testing.T) {
	// Fragment a payload, then reassemble it
	original := make([]byte, 50)
	for i := range original {
		original[i] = byte(i * 3)
	}

	frames := FragmentFastPacket(original, 5)

	a := NewFastPacketAssembler(time.Second)
	now := time.Now()
	pgn := uint32(129794) // AIS static data, a fast-packet PGN

	var result []byte
	for _, frame := range frames {
		result = a.Process(pgn, 1, frame, now)
	}

	if result == nil {
		t.Fatal("reassembly returned nil")
	}
	if len(result) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(result), len(original))
	}
	for i := range original {
		if result[i] != original[i] {
			t.Errorf("byte %d: got 0x%02X, want 0x%02X", i, result[i], original[i])
		}
	}
}

func TestPurgeStale(t *testing.T) {
	a := NewFastPacketAssembler(100 * time.Millisecond)
	now := time.Now()

	frame0 := []byte{0x20, 20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	a.Process(126996, 35, frame0, now)

	if len(a.inProgress) != 1 {
		t.Fatalf("expected 1 in-progress, got %d", len(a.inProgress))
	}

	a.PurgeStale(now.Add(200 * time.Millisecond))

	if len(a.inProgress) != 0 {
		t.Fatalf("expected 0 in-progress after purge, got %d", len(a.inProgress))
	}
}

func TestIsFastPacket(t *testing.T) {
	if !IsFastPacket(129029) {
		t.Error("PGN 129029 should be fast-packet")
	}
	if !IsFastPacket(126996) {
		t.Error("PGN 126996 should be fast-packet")
	}
	if IsFastPacket(129025) {
		t.Error("PGN 129025 should NOT be fast-packet")
	}
	if IsFastPacket(127250) {
		t.Error("PGN 127250 should NOT be fast-packet")
	}
}
