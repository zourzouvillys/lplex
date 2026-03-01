package lplex

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/sixfathoms/lplex/journal"
)

func newConsumerTestBroker() *Broker {
	return NewBroker(BrokerConfig{
		RingSize:          1024,
		MaxBufferDuration: 5 * time.Minute,
		Logger:            slog.Default(),
	})
}

func TestConsumerLiveStream(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	c := b.NewConsumer(ConsumerConfig{Cursor: b.head})
	defer func() { _ = c.Close() }()

	// Inject frames after consumer is created.
	for i := range 5 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := range 5 {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if frame.Header.PGN != 129025 {
			t.Errorf("frame %d: PGN = %d, want 129025", i, frame.Header.PGN)
		}
		if frame.Data[0] != byte(i) {
			t.Errorf("frame %d: data[0] = %d, want %d", i, frame.Data[0], i)
		}
	}

	// Should match cursor
	if c.Cursor() != b.head {
		t.Errorf("cursor = %d, want %d (head)", c.Cursor(), b.head)
	}
}

func TestConsumerCatchUp(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Inject frames before creating consumer.
	for i := range 10 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Consumer starts at seq 1 (beginning of ring).
	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := range 10 {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if frame.Seq != uint64(i+1) {
			t.Errorf("frame %d: seq = %d, want %d", i, frame.Seq, i+1)
		}
	}
}

func TestConsumerFilter(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Inject mixed PGNs.
	injectFrame(b, 129025, 1, []byte{1, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 1, []byte{2, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129025, 1, []byte{3, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129026, 1, []byte{4, 0, 0, 0, 0, 0, 0, 0})
	injectFrame(b, 129025, 1, []byte{5, 0, 0, 0, 0, 0, 0, 0})
	time.Sleep(50 * time.Millisecond)

	// Consumer with PGN filter, starting from beginning.
	c := b.NewConsumer(ConsumerConfig{
		Cursor: 1,
		Filter: &EventFilter{PGNs: []uint32{129025}},
	})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Should only see the 3 frames with PGN 129025.
	for i := range 3 {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if frame.Header.PGN != 129025 {
			t.Errorf("frame %d: PGN = %d, want 129025", i, frame.Header.PGN)
		}
	}
}

func TestConsumerClose(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	c := b.NewConsumer(ConsumerConfig{Cursor: b.head})

	// Close should remove from broker.
	_ = c.Close()

	b.consumerMu.RLock()
	_, exists := b.consumers[c]
	b.consumerMu.RUnlock()
	if exists {
		t.Error("consumer should be removed from broker after Close")
	}

	// Next after close should return error.
	ctx := context.Background()
	_, err := c.Next(ctx)
	if err == nil {
		t.Error("Next after Close should return error")
	}

	// Double close should not panic.
	_ = c.Close()
}

func TestConsumerCloseUnblocksNext(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	c := b.NewConsumer(ConsumerConfig{Cursor: b.head})

	done := make(chan error, 1)
	go func() {
		_, err := c.Next(context.Background())
		done <- err
	}()

	// Give Next a moment to block.
	time.Sleep(50 * time.Millisecond)

	_ = c.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Next should return error when consumer is closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not unblock after Close")
	}
}

func TestConsumerContextCancellation(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	c := b.NewConsumer(ConsumerConfig{Cursor: b.head})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestConsumerFallenBehind(t *testing.T) {
	// Small ring so we can easily overflow it.
	b := NewBroker(BrokerConfig{
		RingSize:          16,
		MaxBufferDuration: time.Minute,
	})
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Consumer starts at seq 1.
	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	// Inject 32 frames to overflow the ring (size 16).
	for i := range 32 {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	ctx := context.Background()
	_, err := c.Next(ctx)
	if !errors.Is(err, ErrFallenBehind) {
		t.Errorf("expected ErrFallenBehind, got %v", err)
	}
}

func TestConsumerConcurrent(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	const numConsumers = 5
	const numFrames = 20

	consumers := make([]*Consumer, numConsumers)
	for i := range numConsumers {
		consumers[i] = b.NewConsumer(ConsumerConfig{Cursor: b.head})
	}

	// Inject frames.
	for i := range numFrames {
		injectFrame(b, 129025, 1, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Each consumer should independently read all frames.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errs := make(chan error, numConsumers)
	for _, c := range consumers {
		go func(c *Consumer) {
			defer func() { _ = c.Close() }()
			for range numFrames {
				if _, err := c.Next(ctx); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(c)
	}

	for range numConsumers {
		if err := <-errs; err != nil {
			t.Errorf("consumer error: %v", err)
		}
	}
}

func TestConsumerFrameJSON(t *testing.T) {
	b := newConsumerTestBroker()
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	c := b.NewConsumer(ConsumerConfig{Cursor: b.head})
	defer func() { _ = c.Close() }()

	injectFrame(b, 129025, 1, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22})
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// JSON should return the cached pre-serialized bytes.
	jsonBytes, err := frame.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonBytes) == 0 {
		t.Error("JSON should return non-empty bytes")
	}

	// Calling again should return the same slice (cached).
	jsonBytes2, _ := frame.JSON()
	if &jsonBytes[0] != &jsonBytes2[0] {
		t.Error("JSON should return cached result")
	}
}

// writeJournalFrames writes frames to a journal in the given directory and returns
// the number of frames written.
func writeJournalFrames(t *testing.T, dir string, frames []RxFrame) {
	t.Helper()
	devices := NewDeviceRegistry()

	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	w, err := NewJournalWriter(JournalConfig{
		Dir:         dir,
		Prefix:      "test",
		BlockSize:   4096,
		Compression: journal.CompressionNone,
	}, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestConsumerJournalFallback(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Write 20 frames to journal with seq 1..20.
	var frames []RxFrame
	for i := range 20 {
		frames = append(frames, makeFrameWithSeq(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0},
			uint64(i+1),
		))
	}
	writeJournalFrames(t, dir, frames)

	// Small ring (16 entries), no frames injected into ring.
	// Broker head starts at 1, but we'll inject frames to push it past 20.
	b := NewBroker(BrokerConfig{
		RingSize:          16,
		MaxBufferDuration: time.Minute,
		JournalDir:        dir,
	})
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Inject 20 frames into the ring to push head to 21 and overflow the ring.
	// Ring size is 16, so tail will advance too. The ring will contain seqs ~5-20.
	for i := range 20 {
		injectFrame(b, 129025, 10, []byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Consumer starts at seq 1, which is behind the ring tail.
	// Should fall back to journal.
	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Read the first 4 frames (which are only in journal, not in ring).
	tail, _ := b.ringRange()
	journalFrameCount := int(tail - 1) // frames before ring tail

	for i := range journalFrameCount {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("journal frame %d: %v", i, err)
		}
		if frame.Seq != uint64(i+1) {
			t.Errorf("journal frame %d: seq = %d, want %d", i, frame.Seq, i+1)
		}
		if frame.Header.PGN != 129025 {
			t.Errorf("journal frame %d: PGN = %d, want 129025", i, frame.Header.PGN)
		}
		if frame.Data[0] != byte(i+1) {
			t.Errorf("journal frame %d: data[0] = %d, want %d", i, frame.Data[0], i+1)
		}
	}

	// After journal frames, should seamlessly read from ring buffer.
	for i := journalFrameCount; i < 20; i++ {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("ring frame %d: %v", i, err)
		}
		if frame.Seq != uint64(i+1) {
			t.Errorf("ring frame %d: seq = %d, want %d", i, frame.Seq, i+1)
		}
	}
}

func TestConsumerJournalToRingTransition(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Write 10 frames to journal with seq 1..10.
	var frames []RxFrame
	for i := range 10 {
		frames = append(frames, makeFrameWithSeq(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0},
			uint64(i+1),
		))
	}
	writeJournalFrames(t, dir, frames)

	// Ring of 8 entries. Inject frames 5..14 (10 frames) so ring holds 7..14.
	b := NewBroker(BrokerConfig{
		RingSize:          8,
		MaxBufferDuration: time.Minute,
		JournalDir:        dir,
	})
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	for i := range 14 {
		injectFrame(b, 129025, 10, []byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Consumer starts at seq 1 (behind ring, in journal territory).
	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Should read all 14 frames seamlessly (journal then ring).
	for i := range 14 {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if frame.Seq != uint64(i+1) {
			t.Errorf("frame %d: seq = %d, want %d", i, frame.Seq, i+1)
		}
	}
}

func TestConsumerJournalFilterApplied(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Write mixed PGN frames to journal.
	journalFrames := []RxFrame{
		makeFrameWithSeq(base, 129025, 10, []byte{1, 0, 0, 0, 0, 0, 0, 0}, 1),
		makeFrameWithSeq(base.Add(time.Millisecond), 129026, 10, []byte{2, 0, 0, 0, 0, 0, 0, 0}, 2),
		makeFrameWithSeq(base.Add(2*time.Millisecond), 129025, 10, []byte{3, 0, 0, 0, 0, 0, 0, 0}, 3),
		makeFrameWithSeq(base.Add(3*time.Millisecond), 129026, 10, []byte{4, 0, 0, 0, 0, 0, 0, 0}, 4),
		makeFrameWithSeq(base.Add(4*time.Millisecond), 129025, 10, []byte{5, 0, 0, 0, 0, 0, 0, 0}, 5),
	}
	writeJournalFrames(t, dir, journalFrames)

	// Ring of 4, inject 8 frames to overflow, ring covers ~5..8.
	b := NewBroker(BrokerConfig{
		RingSize:          4,
		MaxBufferDuration: time.Minute,
		JournalDir:        dir,
	})
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	for i := range 8 {
		pgn := uint32(129025)
		if i%2 == 1 {
			pgn = 129026
		}
		injectFrame(b, pgn, 10, []byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	// Consumer with PGN filter, starting from seq 1.
	c := b.NewConsumer(ConsumerConfig{
		Cursor: 1,
		Filter: &EventFilter{PGNs: []uint32{129025}},
	})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Read frames; all should be PGN 129025.
	for range 4 {
		frame, err := c.Next(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if frame.Header.PGN != 129025 {
			t.Errorf("expected PGN 129025, got %d", frame.Header.PGN)
		}
	}
}

func TestConsumerJournalEmptyDir(t *testing.T) {
	dir := t.TempDir() // empty, no .lpj files

	b := NewBroker(BrokerConfig{
		RingSize:          16,
		MaxBufferDuration: time.Minute,
		JournalDir:        dir,
	})
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	// Overflow the ring.
	for i := range 32 {
		injectFrame(b, 129025, 10, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	_, err := c.Next(context.Background())
	if !errors.Is(err, ErrFallenBehind) {
		t.Errorf("expected ErrFallenBehind with empty journal dir, got %v", err)
	}
}

func TestConsumerJournalFrameJSON(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Write a single frame to journal.
	frames := []RxFrame{
		makeFrameWithSeq(base, 129025, 10, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}, 1),
	}
	writeJournalFrames(t, dir, frames)

	// Small ring, overflow it.
	b := NewBroker(BrokerConfig{
		RingSize:          4,
		MaxBufferDuration: time.Minute,
		JournalDir:        dir,
	})
	go b.Run()
	defer b.CloseRx()
	drainTxFrame(b, time.Second)

	for i := range 8 {
		injectFrame(b, 129025, 10, []byte{byte(i), 0, 0, 0, 0, 0, 0, 0})
	}
	time.Sleep(50 * time.Millisecond)

	c := b.NewConsumer(ConsumerConfig{Cursor: 1})
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	frame, err := c.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Journal frames don't have pre-cached JSON, should serialize on demand.
	jsonBytes, err := frame.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(jsonBytes) == 0 {
		t.Error("JSON should return non-empty bytes")
	}

	// Second call should return cached bytes.
	jsonBytes2, _ := frame.JSON()
	if &jsonBytes[0] != &jsonBytes2[0] {
		t.Error("JSON should return cached result on second call")
	}
}
