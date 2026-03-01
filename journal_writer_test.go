package lplex

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sixfathoms/lplex/journal"
)

// makeFrame builds an RxFrame with a given PGN, source, data, and timestamp.
func makeFrame(t time.Time, pgn uint32, src uint8, data []byte) RxFrame {
	pf := uint8((pgn >> 8) & 0xFF)
	dst := uint8(0xFF)
	if pf < 240 {
		dst = 0xFF
	}
	return RxFrame{
		Timestamp: t,
		Header: CANHeader{
			Priority:    2,
			PGN:         pgn,
			Source:      src,
			Destination: dst,
		},
		Data: data,
	}
}

// makeAddressClaim builds a PGN 60928 address claim frame for a given NAME.
func makeAddressClaim(t time.Time, src uint8, name uint64) RxFrame {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint64(data, name)
	return RxFrame{
		Timestamp: t,
		Header: CANHeader{
			Priority:    2,
			PGN:         60928,
			Source:      src,
			Destination: 0xFF,
		},
		Data: data,
	}
}

// writeAndReadWith is the core helper that writes frames with a given config and reads them back.
func writeAndReadWith(t *testing.T, cfg JournalConfig, frames []RxFrame) []journal.Entry {
	t.Helper()
	devices := NewDeviceRegistry()

	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no journal files written")
	}

	path := filepath.Join(cfg.Dir, entries[0].Name())
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	var result []journal.Entry
	for reader.Next() {
		e := reader.Frame()
		dataCopy := make([]byte, len(e.Data))
		copy(dataCopy, e.Data)
		e.Data = dataCopy
		result = append(result, e)
	}
	if err := reader.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// writeAndRead is a helper that writes frames to a journal and reads them back (uncompressed).
func writeAndRead(t *testing.T, blockSize int, frames []RxFrame) []journal.Entry {
	t.Helper()
	return writeAndReadWith(t, JournalConfig{Dir: t.TempDir(), BlockSize: blockSize}, frames)
}

func TestJournalRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
		makeFrame(base.Add(100*time.Microsecond), 129026, 11, []byte{0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8}),
		makeFrame(base.Add(200*time.Microsecond), 129029, 12, []byte{0x10, 0x20, 0x30, 0x40, 0x50}),
		makeFrame(base.Add(5*time.Second), 60928, 13, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}),
	}

	result := writeAndRead(t, 4096, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}

	for i, got := range result {
		want := frames[i]
		if got.Header.PGN != want.Header.PGN {
			t.Errorf("frame %d: PGN %d, want %d", i, got.Header.PGN, want.Header.PGN)
		}
		if got.Header.Source != want.Header.Source {
			t.Errorf("frame %d: Source %d, want %d", i, got.Header.Source, want.Header.Source)
		}
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch: got %x, want %x", i, got.Data, want.Data)
		}
		if got.Timestamp.UnixMicro() != want.Timestamp.UnixMicro() {
			t.Errorf("frame %d: timestamp %v, want %v", i, got.Timestamp, want.Timestamp)
		}
	}
}

func TestJournalStandardVsExtendedFrames(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	std := makeFrame(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	ext := makeFrame(base.Add(time.Millisecond), 129029, 11, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15})
	short := makeFrame(base.Add(2*time.Millisecond), 59904, 12, []byte{0x00, 0xEE, 0x00})

	result := writeAndRead(t, 4096, []RxFrame{std, ext, short})

	if len(result) != 3 {
		t.Fatalf("got %d frames, want 3", len(result))
	}
	if !bytes.Equal(result[0].Data, std.Data) {
		t.Errorf("standard frame data: got %x, want %x", result[0].Data, std.Data)
	}
	if !bytes.Equal(result[1].Data, ext.Data) {
		t.Errorf("extended frame data: got %x, want %x", result[1].Data, ext.Data)
	}
	if !bytes.Equal(result[2].Data, short.Data) {
		t.Errorf("short frame data: got %x, want %x", result[2].Data, short.Data)
	}
}

func TestJournalDeviceTableWithAddressChanges(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	nameA := uint64(0x1111111111111111)
	nameB := uint64(0x2222222222222222)

	frames := []RxFrame{
		makeAddressClaim(base, 5, nameA),
		makeFrame(base.Add(time.Millisecond), 129025, 5, []byte{1, 2, 3, 4, 5, 6, 7, 8}),
		makeAddressClaim(base.Add(2*time.Millisecond), 5, nameB),
		makeFrame(base.Add(3*time.Millisecond), 129025, 5, []byte{9, 8, 7, 6, 5, 4, 3, 2}),
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()

	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.SeekBlock(0); err != nil {
		t.Fatal(err)
	}

	allDevices := reader.BlockDevices()
	var foundA, foundB bool
	for _, d := range allDevices {
		if d.Source == 5 && d.NAME == nameA {
			foundA = true
		}
		if d.Source == 5 && d.NAME == nameB {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("expected both device A and B in device table, got %+v", allDevices)
	}
}

func TestJournalDeviceTableSeeking(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	nameA := uint64(0xAAAAAAAAAAAAAAAA)
	nameB := uint64(0xBBBBBBBBBBBBBBBB)

	frames := []RxFrame{
		makeAddressClaim(base, 5, nameA),
		makeFrame(base.Add(time.Millisecond), 129025, 5, []byte{1, 2, 3, 4, 5, 6, 7, 8}),
		makeAddressClaim(base.Add(2*time.Millisecond), 5, nameB),
		makeFrame(base.Add(3*time.Millisecond), 129025, 5, []byte{9, 8, 7, 6, 5, 4, 3, 2}),
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()

	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.SeekBlock(0); err != nil {
		t.Fatal(err)
	}

	// At frame 1, device A should be active at source 5
	devs := reader.BlockDevicesAt(1)
	found := false
	for _, d := range devs {
		if d.Source == 5 {
			if d.NAME != nameA {
				t.Errorf("at frame 1, source 5 should be device A, got NAME=%x", d.NAME)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("at frame 1, expected device at source 5")
	}

	// At frame 3, device B should be active at source 5
	devs = reader.BlockDevicesAt(3)
	found = false
	for _, d := range devs {
		if d.Source == 5 {
			if d.NAME != nameB {
				t.Errorf("at frame 3, source 5 should be device B, got NAME=%x", d.NAME)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("at frame 3, expected device at source 5")
	}
}

func TestJournalBlockChecksum(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8}),
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, 1)
	ch <- frames[0]
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	// Corrupt one byte in the block data
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[journal.FileHeaderSize+20]++
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.Next() {
		t.Error("expected Next to fail on corrupted block")
	}
	if reader.Err() == nil {
		t.Error("expected checksum error")
	}
}

func TestJournalBlockBoundary(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	result := writeAndRead(t, 4096, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}

	for i, got := range result {
		want := frames[i]
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch across block boundary", i)
		}
	}
}

func TestJournalTimeSeeking(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*100*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", reader.BlockCount())
	}

	midTime := base.Add(35 * time.Second)
	if err := reader.SeekToTime(midTime); err != nil {
		t.Fatal(err)
	}

	if !reader.Next() {
		t.Fatal("expected to read a frame after seeking")
	}
	first := reader.Frame()
	if first.Timestamp.After(midTime) {
		t.Errorf("first frame after seek is %v, which is after target %v", first.Timestamp, midTime)
	}
}

func TestJournalRotation(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{
		Dir:        dir,
		BlockSize:  4096,
		RotateSize: 4100,
	}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) < 2 {
		t.Fatalf("expected multiple journal files from rotation, got %d", len(entries))
	}

	totalFrames := 0
	for _, e := range entries {
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := journal.NewReader(f)
		if err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		for reader.Next() {
			totalFrames++
		}
		if err := reader.Err(); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		_ = f.Close()
	}

	if totalFrames != len(frames) {
		t.Errorf("total frames across rotated files: %d, want %d", totalFrames, len(frames))
	}
}

func TestJournalCrashResilience(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	origReader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	origBlocks := origReader.BlockCount()
	_ = f.Close()

	if origBlocks < 2 {
		t.Fatalf("need at least 2 blocks, got %d", origBlocks)
	}

	// Truncate to simulate crash: keep header + first block + half of second
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	truncLen := journal.FileHeaderSize + cfg.BlockSize + cfg.BlockSize/2
	if err := os.WriteFile(path, data[:truncLen], 0o644); err != nil {
		t.Fatal(err)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() != 1 {
		t.Errorf("after truncation, block count should be 1, got %d", reader.BlockCount())
	}

	count := 0
	for reader.Next() {
		count++
	}
	if reader.Err() != nil {
		t.Errorf("unexpected error reading surviving block: %v", reader.Err())
	}
	if count == 0 {
		t.Error("expected frames from surviving block")
	}
}

func TestJournalCANIDRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	pdu2 := makeFrame(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	pdu1 := RxFrame{
		Timestamp: base.Add(time.Millisecond),
		Header: CANHeader{
			Priority:    6,
			PGN:         59904,
			Source:      254,
			Destination: 10,
		},
		Data: []byte{0x00, 0xEE, 0x00},
	}

	result := writeAndRead(t, 4096, []RxFrame{pdu2, pdu1})

	if len(result) != 2 {
		t.Fatalf("got %d frames, want 2", len(result))
	}

	if result[0].Header.PGN != 129025 {
		t.Errorf("PDU2 PGN: got %d, want 129025", result[0].Header.PGN)
	}
	if result[0].Header.Source != 10 {
		t.Errorf("PDU2 source: got %d, want 10", result[0].Header.Source)
	}
	if result[0].Header.Destination != 0xFF {
		t.Errorf("PDU2 dest: got %d, want 0xFF", result[0].Header.Destination)
	}
	if result[0].Header.Priority != 2 {
		t.Errorf("PDU2 priority: got %d, want 2", result[0].Header.Priority)
	}

	if result[1].Header.PGN != 59904 {
		t.Errorf("PDU1 PGN: got %d, want 59904", result[1].Header.PGN)
	}
	if result[1].Header.Source != 254 {
		t.Errorf("PDU1 source: got %d, want 254", result[1].Header.Source)
	}
	if result[1].Header.Destination != 10 {
		t.Errorf("PDU1 dest: got %d, want 10", result[1].Header.Destination)
	}
	if result[1].Header.Priority != 6 {
		t.Errorf("PDU1 priority: got %d, want 6", result[1].Header.Priority)
	}
}

func TestJournalBlockChecksumValid(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8}),
		makeFrame(base.Add(time.Millisecond), 129025, 11, []byte{8, 7, 6, 5, 4, 3, 2, 1}),
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, 2)
	ch <- frames[0]
	ch <- frames[1]
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	block := data[journal.FileHeaderSize : journal.FileHeaderSize+4096]
	storedCRC := binary.LittleEndian.Uint32(block[4092:])
	computedCRC := crc32.Checksum(block[:4092], journal.CRC32cTable)
	if storedCRC != computedCRC {
		t.Errorf("CRC mismatch: stored=%08x computed=%08x", storedCRC, computedCRC)
	}
}

func TestJournalEmptyChannel(t *testing.T) {
	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame)
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files for empty channel, got %d", len(entries))
	}
}

// makeProductInfo builds a 134-byte PGN 126996 payload with the given fields.
func makeProductInfo(productCode uint16, modelID, swVersion, modelVersion, serial string) []byte {
	data := make([]byte, 134)
	// bytes 0-1: NMEA 2000 version (unused here)
	binary.LittleEndian.PutUint16(data[2:4], productCode)
	copy(data[4:36], padTo(modelID, 32))
	copy(data[36:76], padTo(swVersion, 40))
	copy(data[76:100], padTo(modelVersion, 24))
	copy(data[100:132], padTo(serial, 32))
	// bytes 132-133: certification level (unused)
	return data
}

func padTo(s string, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xFF
	}
	copy(b, s)
	return b
}

func TestJournalProductInfoRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	nameA := uint64(0x1111111111111111)

	// Pre-populate the device registry with an address claim and product info.
	devices := NewDeviceRegistry()
	claimData := make([]byte, 8)
	binary.LittleEndian.PutUint64(claimData, nameA)
	devices.HandleAddressClaim(10, claimData)
	devices.HandleProductInfo(10, makeProductInfo(4242, "GNX 120", "5.20", "1.0.3", "SN12345"))

	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8}),
		makeFrame(base.Add(time.Millisecond), 129025, 10, []byte{8, 7, 6, 5, 4, 3, 2, 1}),
	}

	dir := t.TempDir()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("no journal files written")
	}

	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.SeekBlock(0); err != nil {
		t.Fatal(err)
	}

	devs := reader.BlockDevices()
	if len(devs) != 1 {
		t.Fatalf("expected 1 device, got %d: %+v", len(devs), devs)
	}

	d := devs[0]
	if d.Source != 10 {
		t.Errorf("Source: got %d, want 10", d.Source)
	}
	if d.NAME != nameA {
		t.Errorf("NAME: got %x, want %x", d.NAME, nameA)
	}
	if d.ProductCode != 4242 {
		t.Errorf("ProductCode: got %d, want 4242", d.ProductCode)
	}
	if d.ModelID != "GNX 120" {
		t.Errorf("ModelID: got %q, want %q", d.ModelID, "GNX 120")
	}
	if d.SoftwareVersion != "5.20" {
		t.Errorf("SoftwareVersion: got %q, want %q", d.SoftwareVersion, "5.20")
	}
	if d.ModelVersion != "1.0.3" {
		t.Errorf("ModelVersion: got %q, want %q", d.ModelVersion, "1.0.3")
	}
	if d.ModelSerial != "SN12345" {
		t.Errorf("ModelSerial: got %q, want %q", d.ModelSerial, "SN12345")
	}
}

func TestJournalProductInfoInBlockChange(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	nameA := uint64(0xAAAAAAAAAAAAAAAA)

	// Device registry starts empty. The address claim frame within the block
	// should capture product info if it's been added to the registry by then.
	devices := NewDeviceRegistry()

	// Pre-register the device so HandleProductInfo works.
	claimData := make([]byte, 8)
	binary.LittleEndian.PutUint64(claimData, nameA)
	devices.HandleAddressClaim(5, claimData)
	devices.HandleProductInfo(5, makeProductInfo(9999, "Pilot", "2.0", "3.1", "XYZ"))

	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8}),
		makeAddressClaim(base.Add(time.Millisecond), 5, nameA),
		makeFrame(base.Add(2*time.Millisecond), 129025, 5, []byte{9, 8, 7, 6, 5, 4, 3, 2}),
	}

	dir := t.TempDir()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.SeekBlock(0); err != nil {
		t.Fatal(err)
	}

	// The in-block address claim for source 5 should have product info.
	devs := reader.BlockDevicesAt(2)
	found := false
	for _, d := range devs {
		if d.Source == 5 {
			found = true
			if d.ProductCode != 9999 {
				t.Errorf("ProductCode: got %d, want 9999", d.ProductCode)
			}
			if d.ModelID != "Pilot" {
				t.Errorf("ModelID: got %q, want %q", d.ModelID, "Pilot")
			}
			if d.ModelSerial != "XYZ" {
				t.Errorf("ModelSerial: got %q, want %q", d.ModelSerial, "XYZ")
			}
		}
	}
	if !found {
		t.Error("device at source 5 not found in device table")
	}
}

// --- Compression tests ---

func TestJournalCompressedRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
		makeFrame(base.Add(100*time.Microsecond), 129026, 11, []byte{0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8}),
		makeFrame(base.Add(200*time.Microsecond), 129029, 12, []byte{0x10, 0x20, 0x30, 0x40, 0x50}),
		makeFrame(base.Add(5*time.Second), 60928, 13, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}),
	}

	cfg := JournalConfig{Dir: t.TempDir(), BlockSize: 4096, Compression: journal.CompressionZstd}
	result := writeAndReadWith(t, cfg, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}

	for i, got := range result {
		want := frames[i]
		if got.Header.PGN != want.Header.PGN {
			t.Errorf("frame %d: PGN %d, want %d", i, got.Header.PGN, want.Header.PGN)
		}
		if got.Header.Source != want.Header.Source {
			t.Errorf("frame %d: Source %d, want %d", i, got.Header.Source, want.Header.Source)
		}
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch: got %x, want %x", i, got.Data, want.Data)
		}
		if got.Timestamp.UnixMicro() != want.Timestamp.UnixMicro() {
			t.Errorf("frame %d: timestamp %v, want %v", i, got.Timestamp, want.Timestamp)
		}
	}
}

func TestJournalCompressedBlockBoundary(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	cfg := JournalConfig{Dir: t.TempDir(), BlockSize: 4096, Compression: journal.CompressionZstd}
	result := writeAndReadWith(t, cfg, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}

	for i, got := range result {
		want := frames[i]
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch across block boundary", i)
		}
	}
}

func TestJournalCompressedTimeSeeking(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*100*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstd}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", reader.BlockCount())
	}

	midTime := base.Add(35 * time.Second)
	if err := reader.SeekToTime(midTime); err != nil {
		t.Fatal(err)
	}

	if !reader.Next() {
		t.Fatal("expected to read a frame after seeking")
	}
	first := reader.Frame()
	if first.Timestamp.After(midTime) {
		t.Errorf("first frame after seek is %v, which is after target %v", first.Timestamp, midTime)
	}
}

func TestJournalCompressedCrashResilience(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstd}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	// Read to get original block count
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	origReader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	origBlocks := origReader.BlockCount()
	_ = f.Close()

	if origBlocks < 2 {
		t.Fatalf("need at least 2 blocks, got %d", origBlocks)
	}

	// Truncate the file: destroy the block index and part of the last block.
	// The forward-scan should recover what it can.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Keep only ~60% of the file to kill the index and at least one block.
	truncLen := len(data) * 6 / 10
	if err := os.WriteFile(path, data[:truncLen], 0o644); err != nil {
		t.Fatal(err)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() == 0 {
		t.Fatal("expected at least one surviving block")
	}
	if reader.BlockCount() >= origBlocks {
		t.Errorf("truncated file should have fewer blocks: got %d, orig %d", reader.BlockCount(), origBlocks)
	}

	// Read all surviving frames
	count := 0
	for reader.Next() {
		count++
	}
	if reader.Err() != nil {
		t.Errorf("unexpected error reading surviving blocks: %v", reader.Err())
	}
	if count == 0 {
		t.Error("expected frames from surviving blocks")
	}
}

func TestJournalBlockIndex(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstd}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	// Verify the block index magic exists at the end of the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8 {
		t.Fatal("file too small")
	}
	tail := data[len(data)-8:]
	count := binary.LittleEndian.Uint32(tail[0:4])
	magic := tail[4:8]
	if !bytes.Equal(magic, journal.BlockIndexMagic[:]) {
		t.Errorf("block index magic: got %x, want %x", magic, journal.BlockIndexMagic)
	}

	// Open and verify the reader can read using the index.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() != int(count) {
		t.Errorf("reader block count %d != index count %d", reader.BlockCount(), count)
	}

	// Read all frames to confirm integrity.
	total := 0
	for reader.Next() {
		total++
	}
	if reader.Err() != nil {
		t.Fatal(reader.Err())
	}
	if total != len(frames) {
		t.Errorf("got %d frames, want %d", total, len(frames))
	}
}

func TestJournalBlockIndexMissing(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstd}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	// Strip the block index from the end of the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Find and remove the index: last 8 bytes are count+magic, then count*8 bytes of offsets.
	tail := data[len(data)-8:]
	indexCount := binary.LittleEndian.Uint32(tail[0:4])
	indexSize := int(indexCount)*8 + 8
	strippedData := data[:len(data)-indexSize]
	if err := os.WriteFile(path, strippedData, 0o644); err != nil {
		t.Fatal(err)
	}

	// The reader should fall back to forward scanning.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() != int(indexCount) {
		t.Errorf("forward-scan block count %d != original %d", reader.BlockCount(), indexCount)
	}

	total := 0
	for reader.Next() {
		total++
	}
	if reader.Err() != nil {
		t.Fatal(reader.Err())
	}
	if total != len(frames) {
		t.Errorf("got %d frames, want %d", total, len(frames))
	}
}

func TestJournalUncompressedStillWorks(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
		makeFrame(base.Add(100*time.Microsecond), 129026, 11, []byte{0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8}),
	}

	// Explicitly CompressionNone (zero value, same as before)
	cfg := JournalConfig{Dir: t.TempDir(), BlockSize: 4096, Compression: journal.CompressionNone}
	result := writeAndReadWith(t, cfg, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}
	for i, got := range result {
		want := frames[i]
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch", i)
		}
	}
}

func TestJournalCompressedSmallerThanUncompressed(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dirUncompressed := t.TempDir()
	dirCompressed := t.TempDir()

	// Write uncompressed
	writeAndReadWith(t, JournalConfig{Dir: dirUncompressed, BlockSize: 4096}, frames)
	// Write compressed
	writeAndReadWith(t, JournalConfig{Dir: dirCompressed, BlockSize: 4096, Compression: journal.CompressionZstd}, frames)

	ucEntries, _ := os.ReadDir(dirUncompressed)
	cEntries, _ := os.ReadDir(dirCompressed)

	ucInfo, _ := ucEntries[0].Info()
	cInfo, _ := cEntries[0].Info()

	t.Logf("uncompressed: %d bytes, compressed: %d bytes, ratio: %.1fx",
		ucInfo.Size(), cInfo.Size(), float64(ucInfo.Size())/float64(cInfo.Size()))

	if cInfo.Size() >= ucInfo.Size() {
		t.Errorf("compressed file (%d) should be smaller than uncompressed (%d)", cInfo.Size(), ucInfo.Size())
	}
}

// --- Dictionary compression tests ---

func TestJournalZstdDictRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	frames := []RxFrame{
		makeFrame(base, 129025, 10, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}),
		makeFrame(base.Add(100*time.Microsecond), 129026, 11, []byte{0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7, 0xA8}),
		makeFrame(base.Add(200*time.Microsecond), 129029, 12, []byte{0x10, 0x20, 0x30, 0x40, 0x50}),
		makeFrame(base.Add(5*time.Second), 60928, 13, []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}),
	}

	cfg := JournalConfig{Dir: t.TempDir(), BlockSize: 4096, Compression: journal.CompressionZstdDict}
	result := writeAndReadWith(t, cfg, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}

	for i, got := range result {
		want := frames[i]
		if got.Header.PGN != want.Header.PGN {
			t.Errorf("frame %d: PGN %d, want %d", i, got.Header.PGN, want.Header.PGN)
		}
		if got.Header.Source != want.Header.Source {
			t.Errorf("frame %d: Source %d, want %d", i, got.Header.Source, want.Header.Source)
		}
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch: got %x, want %x", i, got.Data, want.Data)
		}
		if got.Timestamp.UnixMicro() != want.Timestamp.UnixMicro() {
			t.Errorf("frame %d: timestamp %v, want %v", i, got.Timestamp, want.Timestamp)
		}
	}
}

func TestJournalZstdDictBlockBoundary(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	cfg := JournalConfig{Dir: t.TempDir(), BlockSize: 4096, Compression: journal.CompressionZstdDict}
	result := writeAndReadWith(t, cfg, frames)

	if len(result) != len(frames) {
		t.Fatalf("got %d frames, want %d", len(result), len(frames))
	}

	for i, got := range result {
		want := frames[i]
		if !bytes.Equal(got.Data, want.Data) {
			t.Errorf("frame %d: data mismatch across block boundary", i)
		}
	}
}

func TestJournalZstdDictTimeSeeking(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*100*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstdDict}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() < 2 {
		t.Fatalf("expected at least 2 blocks, got %d", reader.BlockCount())
	}

	midTime := base.Add(35 * time.Second)
	if err := reader.SeekToTime(midTime); err != nil {
		t.Fatal(err)
	}

	if !reader.Next() {
		t.Fatal("expected to read a frame after seeking")
	}
	first := reader.Frame()
	if first.Timestamp.After(midTime) {
		t.Errorf("first frame after seek is %v, which is after target %v", first.Timestamp, midTime)
	}
}

func TestJournalZstdDictCrashResilience(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstdDict}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	origReader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	origBlocks := origReader.BlockCount()
	_ = f.Close()

	if origBlocks < 2 {
		t.Fatalf("need at least 2 blocks, got %d", origBlocks)
	}

	// Truncate: kill the block index and part of the last block.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	truncLen := len(data) * 6 / 10
	if err := os.WriteFile(path, data[:truncLen], 0o644); err != nil {
		t.Fatal(err)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.BlockCount() == 0 {
		t.Fatal("expected at least one surviving block")
	}
	if reader.BlockCount() >= origBlocks {
		t.Errorf("truncated file should have fewer blocks: got %d, orig %d", reader.BlockCount(), origBlocks)
	}

	count := 0
	for reader.Next() {
		count++
	}
	if reader.Err() != nil {
		t.Errorf("unexpected error reading surviving blocks: %v", reader.Err())
	}
	if count == 0 {
		t.Error("expected frames from surviving blocks")
	}
}

func TestJournalZstdDictSmallerThanUncompressed(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 20000 {
		pgn := []uint32{129025, 129026, 127250, 130306, 128267}[i%5]
		src := []uint8{10, 11, 12, 13, 14}[i%5]
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*5*time.Millisecond),
			pgn, src,
			[]byte{byte(i), byte(i >> 8), 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		))
	}

	dirUncompressed := t.TempDir()
	dirZstd := t.TempDir()
	dirDict := t.TempDir()

	writeAndReadWith(t, JournalConfig{Dir: dirUncompressed, BlockSize: 65536}, frames)
	writeAndReadWith(t, JournalConfig{Dir: dirZstd, BlockSize: 65536, Compression: journal.CompressionZstd}, frames)
	writeAndReadWith(t, JournalConfig{Dir: dirDict, BlockSize: 65536, Compression: journal.CompressionZstdDict}, frames)

	sizeOf := func(dir string) int64 {
		entries, _ := os.ReadDir(dir)
		var total int64
		for _, e := range entries {
			info, _ := e.Info()
			total += info.Size()
		}
		return total
	}

	ucTotal := sizeOf(dirUncompressed)
	zstdTotal := sizeOf(dirZstd)
	dictTotal := sizeOf(dirDict)

	t.Logf("uncompressed: %d, plain zstd: %d (%.1fx), zstd+dict: %d (%.1fx)",
		ucTotal, zstdTotal, float64(ucTotal)/float64(zstdTotal),
		dictTotal, float64(ucTotal)/float64(dictTotal))

	// Dict compression must be smaller than uncompressed (the dictionary
	// overhead is significant on synthetic data, but still beats raw).
	if dictTotal >= ucTotal {
		t.Errorf("zstd+dict (%d) should be smaller than uncompressed (%d)", dictTotal, ucTotal)
	}
}

func TestJournalZstdDictInspect(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: journal.CompressionZstdDict}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	f, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	if reader.Compression() != journal.CompressionZstdDict {
		t.Fatalf("compression type: got %d, want %d", reader.Compression(), journal.CompressionZstdDict)
	}

	for i := range reader.BlockCount() {
		bi, err := reader.InspectBlock(i)
		if err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
		if bi.DictLen == 0 {
			t.Errorf("block %d: DictLen should be > 0", i)
		}
		if bi.CompressedLen == 0 {
			t.Errorf("block %d: CompressedLen should be > 0", i)
		}
		if bi.FrameCount == 0 {
			t.Errorf("block %d: FrameCount should be > 0", i)
		}
	}
}

// TestJournalDefaultBlockSizeRoundTrip uses the real default block size (262144)
// which previously caused a uint16 overflow when storing DeviceTableOffset.
func TestJournalDefaultBlockSizeRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Generate enough frames to fill at least one 256KB block.
	// Each standard frame is ~13 bytes, so ~20k frames should do it.
	var frames []RxFrame
	for i := range 20000 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*5*time.Millisecond),
			129025, uint8(10+i%5),
			[]byte{byte(i), byte(i >> 8), 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		))
	}

	for _, compression := range []journal.CompressionType{
		journal.CompressionNone,
		journal.CompressionZstd,
	} {
		t.Run(compressionName(compression), func(t *testing.T) {
			cfg := JournalConfig{
				Dir:         t.TempDir(),
				BlockSize:   262144, // default block size, > uint16 max
				Compression: compression,
			}
			result := writeAndReadWith(t, cfg, frames)

			if len(result) != len(frames) {
				t.Fatalf("got %d frames, want %d", len(result), len(frames))
			}

			for i, got := range result {
				want := frames[i]
				if got.Header.PGN != want.Header.PGN {
					t.Errorf("frame %d: PGN %d, want %d", i, got.Header.PGN, want.Header.PGN)
				}
				if !bytes.Equal(got.Data, want.Data) {
					t.Errorf("frame %d: data mismatch", i)
				}
				if got.Timestamp.UnixMicro() != want.Timestamp.UnixMicro() {
					t.Errorf("frame %d: timestamp mismatch", i)
				}
			}
		})
	}
}

// TestJournalReaderSkipsCorruptedBlock verifies that when a block has corrupted
// frame data (but valid CRC, simulating a logic bug), the reader skips remaining
// frames in that block and continues reading subsequent blocks.
func TestJournalReaderSkipsCorruptedBlock(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Generate enough frames to span multiple blocks at 4096 block size.
	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrame(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
		))
	}

	dir := t.TempDir()
	devices := NewDeviceRegistry()
	ch := make(chan RxFrame, len(frames))
	for _, f := range frames {
		ch <- f
	}
	close(ch)

	cfg := JournalConfig{Dir: dir, BlockSize: 4096}
	w, err := NewJournalWriter(cfg, devices, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	path := filepath.Join(dir, entries[0].Name())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt a structural region in block 1's frame data area by
	// filling it with 0xFF continuation bytes, which breaks varint decoding.
	// Then recompute the CRC so loadBlock succeeds but parseNextFrame fails.
	block1Start := journal.FileHeaderSize + 4096
	for i := 8; i < 64; i++ {
		data[block1Start+i] = 0xFF
	}

	// Recompute CRC for the corrupted block.
	block1 := data[block1Start : block1Start+4096]
	crc := crc32.Checksum(block1[:4092], journal.CRC32cTable)
	binary.LittleEndian.PutUint32(block1[4092:], crc)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	reader, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}

	blockCount := reader.BlockCount()
	if blockCount < 3 {
		t.Fatalf("need at least 3 blocks for this test, got %d", blockCount)
	}

	// Read all frames. Block 1 may be partially or fully skipped,
	// but blocks 0 and 2+ should still read fine.
	count := 0
	for reader.Next() {
		count++
	}
	if reader.Err() != nil {
		t.Fatalf("reader should not have a fatal error, got: %v", reader.Err())
	}

	if count == 0 {
		t.Fatal("expected at least some frames from non-corrupted blocks")
	}
	if count >= len(frames) {
		t.Errorf("expected fewer frames due to corruption, got %d (original %d)", count, len(frames))
	}
}

func compressionName(c journal.CompressionType) string {
	switch c {
	case journal.CompressionNone:
		return "uncompressed"
	case journal.CompressionZstd:
		return "zstd"
	case journal.CompressionZstdDict:
		return "zstd+dict"
	default:
		return "unknown"
	}
}

// makeFrameWithSeq builds an RxFrame with a given seq number.
func makeFrameWithSeq(t time.Time, pgn uint32, src uint8, data []byte, seq uint64) RxFrame {
	f := makeFrame(t, pgn, src, data)
	f.Seq = seq
	return f
}

func TestJournalV2BaseSeqRoundTrip(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Create frames with explicit seq numbers.
	frames := []RxFrame{
		makeFrameWithSeq(base, 129025, 10, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 100),
		makeFrameWithSeq(base.Add(time.Millisecond), 129026, 11, []byte{8, 7, 6, 5, 4, 3, 2, 1}, 101),
		makeFrameWithSeq(base.Add(2*time.Millisecond), 129025, 10, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}, 102),
	}

	for _, compression := range []journal.CompressionType{
		journal.CompressionNone,
		journal.CompressionZstd,
		journal.CompressionZstdDict,
	} {
		t.Run(compressionName(compression), func(t *testing.T) {
			dir := t.TempDir()
			devices := NewDeviceRegistry()
			ch := make(chan RxFrame, len(frames))
			for _, f := range frames {
				ch <- f
			}
			close(ch)

			cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: compression}
			w, err := NewJournalWriter(cfg, devices, ch)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Run(t.Context()); err != nil {
				t.Fatal(err)
			}

			entries, _ := os.ReadDir(dir)
			f, err := os.Open(filepath.Join(dir, entries[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			reader, err := journal.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}

			if reader.Version() != journal.Version2 {
				t.Fatalf("expected version 2, got %d", reader.Version())
			}

			// Read all frames and check FrameSeq.
			var seqs []uint64
			for reader.Next() {
				seqs = append(seqs, reader.FrameSeq())
			}
			if reader.Err() != nil {
				t.Fatal(reader.Err())
			}

			if len(seqs) != 3 {
				t.Fatalf("expected 3 frames, got %d", len(seqs))
			}
			for i, want := range []uint64{100, 101, 102} {
				if seqs[i] != want {
					t.Errorf("frame %d: FrameSeq = %d, want %d", i, seqs[i], want)
				}
			}
		})
	}
}

func TestJournalV2SeekToSeq(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	// Generate enough frames to span multiple blocks.
	var frames []RxFrame
	for i := range 700 {
		frames = append(frames, makeFrameWithSeq(
			base.Add(time.Duration(i)*time.Millisecond),
			129025, 10,
			[]byte{byte(i), byte(i >> 8), 3, 4, 5, 6, 7, 8},
			uint64(1000+i),
		))
	}

	for _, compression := range []journal.CompressionType{
		journal.CompressionNone,
		journal.CompressionZstd,
	} {
		t.Run(compressionName(compression), func(t *testing.T) {
			dir := t.TempDir()
			devices := NewDeviceRegistry()
			ch := make(chan RxFrame, len(frames))
			for _, f := range frames {
				ch <- f
			}
			close(ch)

			cfg := JournalConfig{Dir: dir, BlockSize: 4096, Compression: compression}
			w, err := NewJournalWriter(cfg, devices, ch)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.Run(t.Context()); err != nil {
				t.Fatal(err)
			}

			entries, _ := os.ReadDir(dir)
			f, err := os.Open(filepath.Join(dir, entries[0].Name()))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			reader, err := journal.NewReader(f)
			if err != nil {
				t.Fatal(err)
			}

			if reader.BlockCount() < 2 {
				t.Fatalf("need multiple blocks for this test, got %d", reader.BlockCount())
			}

			// Seek to a seq in the middle.
			targetSeq := uint64(1350)
			if err := reader.SeekToSeq(targetSeq); err != nil {
				t.Fatal(err)
			}

			// The first frame after seeking should have seq <= targetSeq.
			if !reader.Next() {
				t.Fatal("expected a frame after SeekToSeq")
			}
			firstSeq := reader.FrameSeq()
			if firstSeq > targetSeq {
				t.Errorf("first frame seq %d > target %d", firstSeq, targetSeq)
			}

			// Iterate until we find the exact target seq.
			found := firstSeq == targetSeq
			for reader.Next() {
				if reader.FrameSeq() == targetSeq {
					found = true
					break
				}
				if reader.FrameSeq() > targetSeq {
					break
				}
			}
			if !found {
				t.Errorf("target seq %d not found after seeking", targetSeq)
			}
		})
	}
}

func TestJournalV1BackwardCompat(t *testing.T) {
	// Create a minimal v1 journal file by hand and verify the v2 reader can read it.
	dir := t.TempDir()
	path := filepath.Join(dir, "test-v1.lpj")

	blockSize := 4096
	block := make([]byte, blockSize)

	// Build a block with one standard frame.
	baseTimeUs := int64(1750000000000000) // some unix microseconds
	binary.LittleEndian.PutUint64(block[0:8], uint64(baseTimeUs))

	// Frame at offset 8 (v1): delta=0 (varint 0x00), CANID with standard flag, 8 bytes data
	off := 8
	block[off] = 0x00 // delta varint = 0
	off++
	// CAN ID for PGN 129025, src 10, prio 2: build it
	canID := BuildCANID(CANHeader{Priority: 2, PGN: 129025, Source: 10, Destination: 0xFF})
	canID |= 0x80000000 // standard flag
	binary.LittleEndian.PutUint32(block[off:], canID)
	off += 4
	copy(block[off:], []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22})

	// Device table: 0 entries (2 bytes)
	devTableSize := 2
	devTableOff := blockSize - journal.BlockTrailerLen - devTableSize
	binary.LittleEndian.PutUint16(block[devTableOff:], 0)

	// Trailer
	trailerOff := blockSize - journal.BlockTrailerLen
	binary.LittleEndian.PutUint16(block[trailerOff:], uint16(devTableSize))
	binary.LittleEndian.PutUint32(block[trailerOff+2:], 1) // frameCount=1
	checksum := crc32.Checksum(block[:blockSize-4], journal.CRC32cTable)
	binary.LittleEndian.PutUint32(block[blockSize-4:], checksum)

	// Write file: header + block
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [16]byte
	copy(hdr[0:3], journal.Magic[:])
	hdr[3] = journal.Version // v1!
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(blockSize))
	// flags = 0 (CompressionNone)
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(block); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Read it back with the v2-aware reader.
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rf.Close() }()

	reader, err := journal.NewReader(rf)
	if err != nil {
		t.Fatal(err)
	}

	if reader.Version() != journal.Version {
		t.Errorf("version: got %d, want %d (v1)", reader.Version(), journal.Version)
	}
	if reader.BlockCount() != 1 {
		t.Fatalf("block count: got %d, want 1", reader.BlockCount())
	}

	if !reader.Next() {
		t.Fatal("expected one frame")
	}
	entry := reader.Frame()
	if entry.Header.PGN != 129025 {
		t.Errorf("PGN: got %d, want 129025", entry.Header.PGN)
	}
	if entry.Header.Source != 10 {
		t.Errorf("Source: got %d, want 10", entry.Header.Source)
	}
	if !bytes.Equal(entry.Data, []byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}) {
		t.Errorf("Data: got %x", entry.Data)
	}

	// FrameSeq should return 0 for v1 files.
	if reader.FrameSeq() != 0 {
		t.Errorf("FrameSeq should be 0 for v1, got %d", reader.FrameSeq())
	}

	// SeekToSeq should error on v1 files.
	if err := reader.SeekToSeq(1); err == nil {
		t.Error("SeekToSeq should fail on v1 files")
	}

	if reader.Next() {
		t.Error("should not have more frames")
	}
	if reader.Err() != nil {
		t.Fatal(reader.Err())
	}
}
