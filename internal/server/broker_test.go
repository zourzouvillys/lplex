package server

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

func newTestBroker() *Broker {
	return NewBroker(BrokerConfig{
		RingSize:          1024,
		MaxBufferDuration: 5 * time.Minute,
		Logger:            slog.Default(),
	})
}

func injectFrame(b *Broker, pgn uint32, src uint8, data []byte) {
	b.rxFrames <- RxFrame{
		Timestamp: time.Now(),
		Header:    CANHeader{Priority: 2, PGN: pgn, Source: src, Destination: 0xFF},
		Data:      data,
	}
}

func TestBrokerSequenceNumbering(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Create and connect a session
	b.CreateSession("test", time.Minute, nil)
	session, ok := b.ConnectSession("test")
	if !ok {
		t.Fatal("failed to connect session")
	}

	// Inject 3 frames
	for i := range 3 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}

	// Read 3 events from the channel
	for i := range 3 {
		select {
		case data := <-session.Ch:
			var msg frameJSON
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("frame %d: unmarshal error: %v", i, err)
			}
			if msg.Seq != uint64(i+1) {
				t.Errorf("frame %d: seq got %d, want %d", i, msg.Seq, i+1)
			}
			if msg.PGN != 129025 {
				t.Errorf("frame %d: PGN got %d, want 129025", i, msg.PGN)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for frame %d", i)
		}
	}
}

func TestBrokerRingBufferReplay(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Inject frames without any connected client
	for i := range 10 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}

	// Wait for all frames to be processed
	time.Sleep(50 * time.Millisecond)

	// Replay from seq 0 (get everything)
	entries := b.Replay(0, nil)
	if len(entries) != 10 {
		t.Fatalf("expected 10 replay entries, got %d", len(entries))
	}

	// Verify sequence numbers
	for i, data := range entries {
		var msg frameJSON
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal entry %d: %v", i, err)
		}
		if msg.Seq != uint64(i+1) {
			t.Errorf("replay entry %d: seq got %d, want %d", i, msg.Seq, i+1)
		}
	}

	// Replay from seq 5 (get 6-10)
	entries = b.Replay(5, nil)
	if len(entries) != 5 {
		t.Fatalf("expected 5 replay entries from seq 5, got %d", len(entries))
	}
	var firstMsg frameJSON
	if err := json.Unmarshal(entries[0], &firstMsg); err != nil {
		t.Fatalf("unmarshal first entry: %v", err)
	}
	if firstMsg.Seq != 6 {
		t.Errorf("first replay entry: seq got %d, want 6", firstMsg.Seq)
	}
}

func TestBrokerReplayBeyondHead(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	injectFrame(b, 129025, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	// Replay from current head (nothing to replay)
	entries := b.Replay(b.CurrentSeq(), nil)
	if len(entries) != 0 {
		t.Fatalf("expected 0 replay entries, got %d", len(entries))
	}
}

func TestBrokerClientSessionLifecycle(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Create session
	session, seq := b.CreateSession("helm", time.Minute, nil)
	if session.ID != "helm" {
		t.Errorf("session ID: got %q, want %q", session.ID, "helm")
	}
	_ = seq

	// Connect
	session, ok := b.ConnectSession("helm")
	if !ok || !session.Connected {
		t.Fatal("session should be connected")
	}

	// Inject a frame
	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})

	select {
	case <-session.Ch:
		// good
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for frame on connected session")
	}

	// Disconnect
	b.DisconnectSession("helm")

	// Inject another frame while disconnected
	injectFrame(b, 129025, 1, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	// Channel should be empty (not connected)
	select {
	case <-session.Ch:
		t.Fatal("should not receive frames while disconnected")
	default:
		// good
	}
}

func TestBrokerAckAndReplay(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	b.CreateSession("helm", time.Minute, nil)

	// Inject 5 frames
	for i := range 5 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// ACK up to seq 3
	if err := b.AckSession("helm", 3); err != nil {
		t.Fatal(err)
	}

	// Replay should return 4 and 5
	b.sessionMu.RLock()
	cursor := b.sessions["helm"].Cursor
	b.sessionMu.RUnlock()

	entries := b.Replay(cursor, nil)
	if len(entries) != 2 {
		t.Fatalf("expected 2 replay entries after ACK 3, got %d", len(entries))
	}

	var msg frameJSON
	if err := json.Unmarshal(entries[0], &msg); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if msg.Seq != 4 {
		t.Errorf("first replay: seq got %d, want 4", msg.Seq)
	}
}

func TestBrokerAckUnknownSession(t *testing.T) {
	b := newTestBroker()
	err := b.AckSession("nonexistent", 1)
	if err == nil {
		t.Error("expected error for unknown session")
	}
}

func TestBrokerSlowClient(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	b.CreateSession("slow", time.Minute, nil)
	session, _ := b.ConnectSession("slow")

	// Fill the channel buffer (128) and then some
	for i := range 200 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(100 * time.Millisecond)

	// Channel should have at most 128 entries
	count := len(session.Ch)
	if count > 128 {
		t.Errorf("channel should be capped at 128, got %d", count)
	}

	// All 200 frames should be in the ring buffer for replay
	entries := b.Replay(0, nil)
	if len(entries) != 200 {
		t.Errorf("ring buffer should have 200 entries, got %d", len(entries))
	}
}

func TestBrokerDeviceDiscovery(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	b.CreateSession("test", time.Minute, nil)
	session, _ := b.ConnectSession("test")

	// Inject PGN 60928 address claim
	nameBytes := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21 // Garmin
	name |= uint64(150) << 40 // deviceFunction
	name |= uint64(40) << 49  // deviceClass
	putLE64(nameBytes, name)

	injectFrame(b, 60928, 1, nameBytes)

	select {
	case data := <-session.Ch:
		// Should get a device event
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal device event: %v", err)
		}
		if raw["type"] == "device" {
			// Good, got device event
			if raw["manufacturer"] != "Garmin" {
				t.Errorf("manufacturer: got %v, want Garmin", raw["manufacturer"])
			}
		}
		// Also get the frame itself
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for device event")
	}

	// Device registry should have the device
	devices := b.devices.Snapshot()
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].Manufacturer != "Garmin" {
		t.Errorf("device manufacturer: got %q, want Garmin", devices[0].Manufacturer)
	}
}

func TestBrokerBufferTimeoutCap(t *testing.T) {
	b := NewBroker(BrokerConfig{
		RingSize:          1024,
		MaxBufferDuration: time.Minute,
	})

	session, _ := b.CreateSession("test", 10*time.Minute, nil)
	if session.BufferTimeout != time.Minute {
		t.Errorf("buffer timeout should be capped at 1m, got %v", session.BufferTimeout)
	}
}

func TestBrokerReconnectSession(t *testing.T) {
	b := newTestBroker()

	// Create same session twice should return same session
	s1, _ := b.CreateSession("helm", time.Minute, nil)
	s2, _ := b.CreateSession("helm", 2*time.Minute, nil)

	if s1 != s2 {
		t.Error("reconnecting should return the same session")
	}
	if s2.BufferTimeout != 2*time.Minute {
		t.Errorf("buffer timeout should be updated to 2m, got %v", s2.BufferTimeout)
	}
}

func TestBrokerConnectUnknownSession(t *testing.T) {
	b := newTestBroker()
	_, ok := b.ConnectSession("nonexistent")
	if ok {
		t.Error("connecting unknown session should fail")
	}
}

func TestBrokerRingOverwrite(t *testing.T) {
	// Small ring to force overwrite
	b := NewBroker(BrokerConfig{
		RingSize:          16,
		MaxBufferDuration: time.Minute,
	})
	go b.Run()
	defer close(b.rxFrames)

	// Inject 32 frames (2x the ring size)
	for i := range 32 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Replay from 0 should only get the last 16
	entries := b.Replay(0, nil)
	if len(entries) != 16 {
		t.Fatalf("expected 16 replay entries after overwrite, got %d", len(entries))
	}

	var first frameJSON
	if err := json.Unmarshal(entries[0], &first); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	if first.Seq != 17 {
		t.Errorf("first entry after overwrite: seq got %d, want 17", first.Seq)
	}
}

func putLE64(b []byte, v uint64) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	b[4] = byte(v >> 32)
	b[5] = byte(v >> 40)
	b[6] = byte(v >> 48)
	b[7] = byte(v >> 56)
}

// drainTxFrame reads a single frame from txFrames with a timeout.
// Returns the frame and true if one was available, or zero value and false on timeout.
func drainTxFrame(b *Broker, timeout time.Duration) (TxRequest, bool) {
	select {
	case f := <-b.txFrames:
		return f, true
	case <-time.After(timeout):
		return TxRequest{}, false
	}
}

func TestBrokerStartupBroadcastsISORequest(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	tx, ok := drainTxFrame(b, time.Second)
	if !ok {
		t.Fatal("expected startup ISO Request, got nothing")
	}
	if tx.Header.PGN != 59904 {
		t.Errorf("PGN: got %d, want 59904", tx.Header.PGN)
	}
	if tx.Header.Source != 254 {
		t.Errorf("source: got %d, want 254", tx.Header.Source)
	}
	if tx.Header.Destination != 0xFF {
		t.Errorf("destination: got %d, want 255 (broadcast)", tx.Header.Destination)
	}
	if len(tx.Data) != 3 || tx.Data[0] != 0x00 || tx.Data[1] != 0xEE || tx.Data[2] != 0x00 {
		t.Errorf("data: got %x, want 00ee00", tx.Data)
	}
}

func TestBrokerNewSourceTriggersISORequest(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Drain the startup broadcast.
	drainTxFrame(b, time.Second)

	// Inject a non-address-claim frame from a new source.
	injectFrame(b, 129025, 42, []byte{0, 0, 0, 0, 0, 0, 0, 0})

	tx, ok := drainTxFrame(b, time.Second)
	if !ok {
		t.Fatal("expected targeted ISO Request for new source, got nothing")
	}
	if tx.Header.PGN != 59904 {
		t.Errorf("PGN: got %d, want 59904", tx.Header.PGN)
	}
	if tx.Header.Destination != 42 {
		t.Errorf("destination: got %d, want 42", tx.Header.Destination)
	}
}

func TestBrokerAddressClaimTriggersProductInfoRequest(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)

	// Drain the startup broadcast.
	drainTxFrame(b, time.Second)

	// Inject PGN 60928 (address claim) from a new source.
	nameBytes := make([]byte, 8)
	putLE64(nameBytes, uint64(229)<<21) // Garmin
	injectFrame(b, 60928, 10, nameBytes)

	// Should get an ISO Request for PGN 126996 (Product Information).
	tx, ok := drainTxFrame(b, time.Second)
	if !ok {
		t.Fatal("expected ISO Request for Product Info after address claim, got nothing")
	}
	if tx.Header.PGN != 59904 {
		t.Errorf("PGN: got %d, want 59904 (ISO Request)", tx.Header.PGN)
	}
	if tx.Header.Destination != 10 {
		t.Errorf("destination: got %d, want 10", tx.Header.Destination)
	}
	// PGN 126996 = 0x1F014, LE bytes: 0x14, 0xF0, 0x01
	if len(tx.Data) != 3 || tx.Data[0] != 0x14 || tx.Data[1] != 0xF0 || tx.Data[2] != 0x01 {
		t.Errorf("data should encode PGN 126996: got %x, want 14f001", tx.Data)
	}

	// No additional spurious requests.
	time.Sleep(50 * time.Millisecond)
	select {
	case extra := <-b.txFrames:
		t.Errorf("unexpected extra ISO Request: PGN=%d dst=%d data=%x", extra.Header.PGN, extra.Header.Destination, extra.Data)
	default:
	}
}

// registerDevice is a helper that injects a PGN 60928 address claim for a device
// and waits for the broker to process it.
func registerDevice(b *Broker, src uint8, manufacturerCode uint16, instance uint8) {
	nameBytes := make([]byte, 8)
	var name uint64
	name |= uint64(manufacturerCode) << 21
	name |= uint64(instance) << 32
	putLE64(nameBytes, name)
	injectFrame(b, 60928, src, nameBytes)
	time.Sleep(50 * time.Millisecond)
	// Drain the ISO Request for product info that follows address claim.
	drainTxFrame(b, 100*time.Millisecond)
}

func TestBrokerFilterByPGN(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	filter := &EventFilter{PGNs: []uint32{129025}}
	b.CreateSession("filtered", time.Minute, filter)
	session, _ := b.ConnectSession("filtered")

	// Inject a frame that matches the filter.
	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	// Inject a frame that does NOT match.
	injectFrame(b, 129026, 1, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	// Should only get the matching frame.
	select {
	case data := <-session.Ch:
		var msg frameJSON
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.PGN != 129025 {
			t.Errorf("expected PGN 129025, got %d", msg.PGN)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for filtered frame")
	}

	select {
	case data := <-session.Ch:
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		t.Errorf("should not receive PGN %d, filter is [129025]", msg.PGN)
	case <-time.After(100 * time.Millisecond):
		// good
	}
}

func TestBrokerFilterByManufacturer(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Register a Garmin device at src 5.
	registerDevice(b, 5, 229, 0)

	filter := &EventFilter{Manufacturers: []string{"Garmin"}}
	b.CreateSession("mfr-filter", time.Minute, filter)
	session, _ := b.ConnectSession("mfr-filter")

	// Frame from Garmin (src 5) should pass.
	injectFrame(b, 129025, 5, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	// Frame from unknown src 10 should be dropped.
	injectFrame(b, 129025, 10, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	select {
	case data := <-session.Ch:
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		if msg.Src != 5 {
			t.Errorf("expected src 5 (Garmin), got %d", msg.Src)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for Garmin frame")
	}

	// Second frame should NOT arrive (src 10 is not Garmin).
	// Note: src 10 is new, so the broker will send an ISO Request.
	// Drain that, then check the channel.
	drainTxFrame(b, 100*time.Millisecond)

	select {
	case data := <-session.Ch:
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		// Might be a device event from address claim of src 10 (which
		// won't have "type" field, so it's a frame), or an actual frame.
		if msg.Src == 10 && msg.PGN == 129025 {
			t.Error("should not receive frames from non-Garmin device")
		}
	case <-time.After(100 * time.Millisecond):
		// good
	}
}

func TestBrokerFilterByManufacturerCode(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Register Garmin (code 229) at src 5, Victron (code 358) at src 7.
	registerDevice(b, 5, 229, 0)
	registerDevice(b, 7, 358, 0)

	// Filter by numeric code "229" instead of name "Garmin".
	filter := &EventFilter{Manufacturers: []string{"229"}}
	b.CreateSession("code-filter", time.Minute, filter)
	session, _ := b.ConnectSession("code-filter")

	injectFrame(b, 129025, 5, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129025, 7, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	select {
	case data := <-session.Ch:
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		if msg.Src != 5 {
			t.Errorf("expected src 5 (Garmin/229), got %d", msg.Src)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for frame matching manufacturer code 229")
	}

	select {
	case data := <-session.Ch:
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		if msg.Src == 7 {
			t.Error("should not receive frames from Victron when filtering by code 229")
		}
	case <-time.After(100 * time.Millisecond):
		// good
	}
}

func TestBrokerFilterByInstance(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Register device at src 3 with instance 2 (Garmin).
	registerDevice(b, 3, 229, 2)

	filter := &EventFilter{Instances: []uint8{2}}
	b.CreateSession("inst-filter", time.Minute, filter)
	session, _ := b.ConnectSession("inst-filter")

	// Frame from instance 2 device should pass.
	injectFrame(b, 129025, 3, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	select {
	case data := <-session.Ch:
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		if msg.Src != 3 {
			t.Errorf("expected src 3, got %d", msg.Src)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for instance-filtered frame")
	}
}

func TestBrokerFilterCombined(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Register Garmin at src 5, Victron at src 7.
	registerDevice(b, 5, 229, 0)
	registerDevice(b, 7, 358, 0)

	// Filter: PGN 129025 from Garmin only.
	filter := &EventFilter{
		PGNs:          []uint32{129025},
		Manufacturers: []string{"Garmin"},
	}
	b.CreateSession("combo", time.Minute, filter)
	session, _ := b.ConnectSession("combo")

	// PGN 129025 from Garmin -> pass.
	injectFrame(b, 129025, 5, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	// PGN 129026 from Garmin -> blocked (wrong PGN).
	injectFrame(b, 129026, 5, []byte{2, 0, 0, 0, 0, 0, 0, 0})
	// PGN 129025 from Victron -> blocked (wrong manufacturer).
	injectFrame(b, 129025, 7, []byte{3, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	var received []frameJSON
	for {
		select {
		case data := <-session.Ch:
			var msg frameJSON
			_ = json.Unmarshal(data, &msg)
			received = append(received, msg)
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	if len(received) != 1 {
		t.Fatalf("expected 1 frame, got %d: %+v", len(received), received)
	}
	if received[0].PGN != 129025 || received[0].Src != 5 {
		t.Errorf("unexpected frame: PGN=%d src=%d", received[0].PGN, received[0].Src)
	}
}

func TestBrokerReplayWithFilter(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Register Garmin at src 5.
	registerDevice(b, 5, 229, 0)

	// Inject mixed frames (no session connected, they go to ring only).
	injectFrame(b, 129025, 5, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 5, []byte{2, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129025, 10, []byte{3, 0, 0, 0, 0, 0, 0, 0})
	// Drain the ISO Request for new source 10.
	drainTxFrame(b, 100*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Replay with PGN filter.
	filter := &EventFilter{PGNs: []uint32{129025}}
	entries := b.Replay(0, filter)

	// Should get frames with PGN 129025 only (from both sources).
	if len(entries) != 2 {
		t.Fatalf("expected 2 filtered replay entries, got %d", len(entries))
	}

	for _, data := range entries {
		var msg frameJSON
		_ = json.Unmarshal(data, &msg)
		if msg.PGN != 129025 {
			t.Errorf("replay should only contain PGN 129025, got %d", msg.PGN)
		}
	}

	// Replay with manufacturer filter.
	mfrFilter := &EventFilter{Manufacturers: []string{"Garmin"}}
	entries = b.Replay(0, mfrFilter)
	// Frames from src 5 (Garmin): address claim + 129025 + 129026 = 3.
	// Src 10 is unknown, so excluded.
	if len(entries) != 3 {
		t.Fatalf("expected 3 Garmin replay entries, got %d", len(entries))
	}
}

func TestBrokerBufferTimeoutZeroResetsCursor(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Create session, inject frames, ACK some.
	b.CreateSession("reset-test", time.Minute, nil)
	b.ConnectSession("reset-test")

	for i := range 5 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	if err := b.AckSession("reset-test", 3); err != nil {
		t.Fatalf("ack: %v", err)
	}
	b.DisconnectSession("reset-test")

	// Recreate with buffer_timeout=0 -> cursor should reset.
	session, _ := b.CreateSession("reset-test", 0, nil)
	if session.Cursor != 0 {
		t.Errorf("cursor should be 0 after reset, got %d", session.Cursor)
	}

	// Connect: no replay should happen (cursor is 0).
	session, _ = b.ConnectSession("reset-test")
	// Channel should be empty (no replayed frames).
	select {
	case <-session.Ch:
		t.Error("should not receive replayed frames after buffer_timeout=0 reset")
	case <-time.After(100 * time.Millisecond):
		// good
	}
}

func TestBrokerNilFilterReceivesAll(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	b.CreateSession("all", time.Minute, nil)
	session, _ := b.ConnectSession("all")

	injectFrame(b, 129025, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 2, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 130311, 3, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	count := 0
	for {
		select {
		case <-session.Ch:
			count++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	if count != 3 {
		t.Errorf("nil filter should receive all frames, got %d", count)
	}
}

func TestBrokerDeviceEventsBypassFilter(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	// Session with PGN filter that would NOT match PGN 60928.
	filter := &EventFilter{PGNs: []uint32{129025}}
	b.CreateSession("device-bypass", time.Minute, filter)
	session, _ := b.ConnectSession("device-bypass")

	// Inject address claim (PGN 60928).
	nameBytes := make([]byte, 8)
	putLE64(nameBytes, uint64(229)<<21) // Garmin
	injectFrame(b, 60928, 5, nameBytes)
	drainTxFrame(b, 100*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	// Should get a device event (sent via fanOutDevice, unfiltered).
	gotDevice := false
	for {
		select {
		case data := <-session.Ch:
			var raw map[string]any
			_ = json.Unmarshal(data, &raw)
			if raw["type"] == "device" {
				gotDevice = true
			}
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	if !gotDevice {
		t.Error("device events should bypass PGN filter")
	}
}

func TestBrokerSubscriber(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	sub, cleanup := b.Subscribe(nil)
	defer cleanup()

	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 2, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	count := 0
	for {
		select {
		case <-sub.ch:
			count++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	if count != 2 {
		t.Errorf("subscriber should receive all frames, got %d", count)
	}
}

func TestBrokerSubscriberWithFilter(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	filter := &EventFilter{PGNs: []uint32{129025}}
	sub, cleanup := b.Subscribe(filter)
	defer cleanup()

	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 2, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	count := 0
	for {
		select {
		case <-sub.ch:
			count++
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	if count != 1 {
		t.Errorf("filtered subscriber should receive 1 frame, got %d", count)
	}
}

func TestBrokerSubscriberCleanup(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	sub, cleanup := b.Subscribe(nil)

	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	select {
	case <-sub.ch:
		// good, received before cleanup
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for frame before cleanup")
	}

	cleanup()

	// After cleanup, subscriber should not be in the map.
	b.subscriberMu.RLock()
	_, exists := b.subscribers[sub]
	b.subscriberMu.RUnlock()
	if exists {
		t.Error("subscriber should be removed after cleanup")
	}
}

func TestBrokerSubscriberReceivesDeviceEvents(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer close(b.rxFrames)
	drainTxFrame(b, time.Second)

	sub, cleanup := b.Subscribe(nil)
	defer cleanup()

	nameBytes := make([]byte, 8)
	putLE64(nameBytes, uint64(229)<<21) // Garmin
	injectFrame(b, 60928, 5, nameBytes)
	drainTxFrame(b, 100*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	gotDevice := false
	for {
		select {
		case data := <-sub.ch:
			var raw map[string]any
			_ = json.Unmarshal(data, &raw)
			if raw["type"] == "device" {
				gotDevice = true
			}
		case <-time.After(100 * time.Millisecond):
			goto done
		}
	}
done:

	if !gotDevice {
		t.Error("subscriber should receive device events")
	}
}
