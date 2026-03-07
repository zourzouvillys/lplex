package lplex

import (
	"context"
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
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1})
	defer func() { _ = c.Close() }()

	for i := range 3 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := range 3 {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if frame.Seq != uint64(i+1) {
			t.Errorf("frame %d: seq got %d, want %d", i, frame.Seq, i+1)
		}
		if frame.Header.PGN != 129025 {
			t.Errorf("frame %d: PGN got %d, want 129025", i, frame.Header.PGN)
		}
	}
}

func TestBrokerAckAndReplay(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	b.CreateSession("helm", time.Minute, nil)

	for i := range 5 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// ACK up to seq 3
	if err := b.AckSession("helm", 3); err != nil {
		t.Fatal(err)
	}

	// Consumer starting after ACK'd cursor should read 4 and 5
	session := b.GetSession("helm")
	c := b.NewConsumer(ConsumerConfig{Cursor: session.Cursor + 1})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Seq != 4 {
		t.Errorf("first frame: seq got %d, want 4", frame.Seq)
	}

	frame, err = c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Seq != 5 {
		t.Errorf("second frame: seq got %d, want 5", frame.Seq)
	}
}

func TestBrokerAckUnknownSession(t *testing.T) {
	b := newTestBroker()
	err := b.AckSession("nonexistent", 1)
	if err == nil {
		t.Error("expected error for unknown session")
	}
}

func TestBrokerDeviceDiscovery(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Inject PGN 60928 address claim
	nameBytes := make([]byte, 8)
	var name uint64
	name |= uint64(229) << 21 // Garmin
	name |= uint64(150) << 40 // deviceFunction
	name |= uint64(40) << 49  // deviceClass
	putLE64(nameBytes, name)

	injectFrame(b, 60928, 1, nameBytes)
	// Drain the product info ISO request.
	drainTxFrame(b, time.Second)
	time.Sleep(50 * time.Millisecond)

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

func TestBrokerFilterByManufacturer(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Register a Garmin device at src 5.
	registerDevice(b, 5, 229, 0)

	filter := &EventFilter{Manufacturers: []string{"Garmin"}}
	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1, Filter: filter})
	defer func() { _ = c.Close() }()

	// Frame from Garmin (src 5) should pass.
	injectFrame(b, 129025, 5, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	// Frame from unknown src 10 should be dropped.
	injectFrame(b, 129025, 10, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	drainTxFrame(b, 100*time.Millisecond) // drain ISO request for src 10
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.Source != 5 {
		t.Errorf("expected src 5 (Garmin), got %d", frame.Header.Source)
	}
}

func TestBrokerFilterByManufacturerCode(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Register Garmin (code 229) at src 5, Victron (code 358) at src 7.
	registerDevice(b, 5, 229, 0)
	registerDevice(b, 7, 358, 0)

	// Filter by numeric code "229" instead of name "Garmin".
	filter := &EventFilter{Manufacturers: []string{"229"}}
	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1, Filter: filter})
	defer func() { _ = c.Close() }()

	injectFrame(b, 129025, 5, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129025, 7, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.Source != 5 {
		t.Errorf("expected src 5 (Garmin/229), got %d", frame.Header.Source)
	}
}

func TestBrokerFilterByInstance(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Register device at src 3 with instance 2 (Garmin).
	registerDevice(b, 3, 229, 2)

	filter := &EventFilter{Instances: []uint8{2}}
	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1, Filter: filter})
	defer func() { _ = c.Close() }()

	// Frame from instance 2 device should pass.
	injectFrame(b, 129025, 3, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.Source != 3 {
		t.Errorf("expected src 3, got %d", frame.Header.Source)
	}
}

func TestBrokerFilterCombined(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Register Garmin at src 5, Victron at src 7.
	registerDevice(b, 5, 229, 0)
	registerDevice(b, 7, 358, 0)

	// Filter: PGN 129025 from Garmin only.
	filter := &EventFilter{
		PGNs:          []uint32{129025},
		Manufacturers: []string{"Garmin"},
	}
	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1, Filter: filter})
	defer func() { _ = c.Close() }()

	// PGN 129025 from Garmin -> pass.
	injectFrame(b, 129025, 5, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	// PGN 129026 from Garmin -> blocked (wrong PGN).
	injectFrame(b, 129026, 5, []byte{2, 0, 0, 0, 0, 0, 0, 0})
	// PGN 129025 from Victron -> blocked (wrong manufacturer).
	injectFrame(b, 129025, 7, []byte{3, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.PGN != 129025 || frame.Header.Source != 5 {
		t.Errorf("unexpected frame: PGN=%d src=%d", frame.Header.PGN, frame.Header.Source)
	}
}

func TestBrokerFilterExcludePGN(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Exclude PGN 60928 (address claim) and 126996 (product info).
	filter := &EventFilter{ExcludePGNs: []uint32{60928, 126996}}
	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1, Filter: filter})
	defer func() { _ = c.Close() }()

	// Excluded PGN 60928 should be dropped.
	injectFrame(b, 60928, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	// Excluded PGN 126996 should be dropped.
	injectFrame(b, 126996, 1, []byte{0, 0, 0, 0, 0, 0, 0, 0})
	// Non-excluded PGN 129025 should pass.
	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	// Another non-excluded PGN should pass.
	injectFrame(b, 130306, 2, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.PGN != 129025 {
		t.Errorf("first frame PGN = %d, want 129025", frame.Header.PGN)
	}

	frame, err = c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.PGN != 130306 {
		t.Errorf("second frame PGN = %d, want 130306", frame.Header.PGN)
	}
}

func TestBrokerFilterExcludeWithInclude(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Include PGN 129025 and 129026, but exclude 129026.
	// Exclude takes precedence: only 129025 should pass.
	filter := &EventFilter{
		PGNs:        []uint32{129025, 129026},
		ExcludePGNs: []uint32{129026},
	}
	c := b.NewConsumer(ConsumerConfig{Cursor: b.CurrentSeq() + 1, Filter: filter})
	defer func() { _ = c.Close() }()

	injectFrame(b, 129025, 1, []byte{0xAA, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 1, []byte{0xBB, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 130306, 1, []byte{0xCC, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.PGN != 129025 {
		t.Errorf("frame PGN = %d, want 129025", frame.Header.PGN)
	}

	// No more frames should arrive within a short timeout.
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer shortCancel()
	_, err = c.Next(shortCtx)
	if err == nil {
		t.Error("expected timeout, got unexpected frame")
	}
}

func TestBrokerBufferTimeoutZeroResetsCursor(t *testing.T) {
	b := newTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Create session, inject frames, ACK some.
	b.CreateSession("reset-test", time.Minute, nil)

	for i := range 5 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	if err := b.AckSession("reset-test", 3); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// Recreate with buffer_timeout=0 -> cursor should reset.
	session, _ := b.CreateSession("reset-test", 0, nil)
	if session.Cursor != 0 {
		t.Errorf("cursor should be 0 after reset, got %d", session.Cursor)
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

// --- ReplicaMode tests ---

func newReplicaBroker(initialHead uint64) *Broker {
	return NewBroker(BrokerConfig{
		RingSize:    1024,
		ReplicaMode: true,
		InitialHead: initialHead,
		Logger:      slog.Default(),
	})
}

func injectReplicaFrame(b *Broker, seq uint64, pgn uint32, src uint8) {
	b.rxFrames <- RxFrame{
		Timestamp: time.Now(),
		Header:    CANHeader{Priority: 2, PGN: pgn, Source: src, Destination: 0xFF},
		Data:      []byte{byte(seq), 1, 2, 3, 4, 5, 6, 7},
		Seq:       seq,
	}
}

func TestBrokerReplicaModeHonorsFrameSeq(t *testing.T) {
	b := newReplicaBroker(1)
	go b.Run()
	defer b.CloseRx()

	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	// Send frames with specific sequence numbers
	for _, seq := range []uint64{1, 2, 3, 4, 5} {
		injectReplicaFrame(b, seq, 129025, 1)
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for expected := uint64(1); expected <= 5; expected++ {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", expected, err)
		}
		if frame.Seq != expected {
			t.Fatalf("expected seq %d, got %d", expected, frame.Seq)
		}
	}
}

func TestBrokerReplicaModeHeadAdvancement(t *testing.T) {
	b := newReplicaBroker(1)
	go b.Run()
	defer b.CloseRx()

	// Initially head is 1, so CurrentSeq should be 0 (head-1)
	if got := b.CurrentSeq(); got != 0 {
		t.Fatalf("initial CurrentSeq: got %d, want 0", got)
	}

	injectReplicaFrame(b, 5, 129025, 1)
	time.Sleep(50 * time.Millisecond)

	// Head should jump to 6 (5+1), so CurrentSeq = 5
	if got := b.CurrentSeq(); got != 5 {
		t.Fatalf("after seq 5: CurrentSeq got %d, want 5", got)
	}

	// Sending a lower seq shouldn't move head backwards
	injectReplicaFrame(b, 3, 129025, 1)
	time.Sleep(50 * time.Millisecond)

	if got := b.CurrentSeq(); got != 5 {
		t.Fatalf("after seq 3: CurrentSeq got %d, want 5 (head shouldn't go backwards)", got)
	}

	// Sending a higher seq should advance head
	injectReplicaFrame(b, 10, 129025, 1)
	time.Sleep(50 * time.Millisecond)

	if got := b.CurrentSeq(); got != 10 {
		t.Fatalf("after seq 10: CurrentSeq got %d, want 10", got)
	}
}

func TestBrokerReplicaModeNoISORequests(t *testing.T) {
	b := newReplicaBroker(1)
	go b.Run()
	defer b.CloseRx()

	// In replica mode, no startup ISO broadcast
	_, ok := drainTxFrame(b, 200*time.Millisecond)
	if ok {
		t.Fatal("replica mode should not send startup ISO Request")
	}

	// New source should not trigger ISO Request in replica mode
	injectReplicaFrame(b, 1, 129025, 42)
	time.Sleep(50 * time.Millisecond)

	_, ok = drainTxFrame(b, 200*time.Millisecond)
	if ok {
		t.Fatal("replica mode should not send ISO Request for new source")
	}
}

func TestBrokerReplicaModeWithGaps(t *testing.T) {
	b := newReplicaBroker(1)
	go b.Run()
	defer b.CloseRx()

	// Send non-contiguous frames (simulating replication gaps)
	injectReplicaFrame(b, 1, 129025, 1)
	injectReplicaFrame(b, 2, 129025, 1)
	injectReplicaFrame(b, 5, 129025, 1) // skip 3, 4
	injectReplicaFrame(b, 6, 129025, 1)
	time.Sleep(50 * time.Millisecond)

	if got := b.CurrentSeq(); got != 6 {
		t.Fatalf("CurrentSeq: got %d, want 6", got)
	}

	// Verify we can read the frames that exist
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Seq != 1 {
		t.Fatalf("first frame: got seq %d, want 1", frame.Seq)
	}

	frame, err = c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Seq != 2 {
		t.Fatalf("second frame: got seq %d, want 2", frame.Seq)
	}
}

func TestBrokerInitialHead(t *testing.T) {
	b := newReplicaBroker(5000)
	go b.Run()
	defer b.CloseRx()

	// CurrentSeq should reflect the initial head minus 1
	if got := b.CurrentSeq(); got != 4999 {
		t.Fatalf("initial CurrentSeq: got %d, want 4999", got)
	}

	// New frames should continue from the initial head
	injectReplicaFrame(b, 5000, 129025, 1)
	injectReplicaFrame(b, 5001, 129025, 1)
	time.Sleep(50 * time.Millisecond)

	if got := b.CurrentSeq(); got != 5001 {
		t.Fatalf("after frames: CurrentSeq got %d, want 5001", got)
	}

	// Consumer starting from 5000 should read the new frames
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := b.NewConsumer(ConsumerConfig{Cursor: 5000})
	defer func() { _ = c.Close() }()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Seq != 5000 {
		t.Fatalf("expected seq 5000, got %d", frame.Seq)
	}
}

func TestBrokerInitialHeadZeroDefaultsToOne(t *testing.T) {
	b := NewBroker(BrokerConfig{
		RingSize:    1024,
		ReplicaMode: true,
		InitialHead: 0, // zero means start at 1
		Logger:      slog.Default(),
	})
	go b.Run()
	defer b.CloseRx()

	if got := b.CurrentSeq(); got != 0 {
		t.Fatalf("zero InitialHead should start at head=1 (CurrentSeq=0), got %d", got)
	}
}

func TestBrokerReplicaModeRingWrap(t *testing.T) {
	// Small ring to test wraparound
	b := NewBroker(BrokerConfig{
		RingSize:    16,
		ReplicaMode: true,
		InitialHead: 1,
		Logger:      slog.Default(),
	})
	go b.Run()
	defer b.CloseRx()

	// Fill more than the ring size
	for seq := uint64(1); seq <= 32; seq++ {
		injectReplicaFrame(b, seq, 129025, 1)
	}
	time.Sleep(50 * time.Millisecond)

	if got := b.CurrentSeq(); got != 32 {
		t.Fatalf("CurrentSeq: got %d, want 32", got)
	}

	// Recent frames should still be readable
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := b.NewConsumer(ConsumerConfig{Cursor: 25})
	defer func() { _ = c.Close() }()

	for expected := uint64(25); expected <= 32; expected++ {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", expected, err)
		}
		if frame.Seq != expected {
			t.Fatalf("expected seq %d, got %d", expected, frame.Seq)
		}
	}
}
