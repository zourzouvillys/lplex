package lplex

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sixfathoms/lplex/journal"
)

func TestBlockWriter_UncompressedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bw, err := NewBlockWriter(BlockWriterConfig{
		Dir:         dir,
		BlockSize:   4096,
		Compression: journal.CompressionNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Build a valid uncompressed block with one frame.
	block := makeTestBlock(4096, 1000, 42)

	if err := bw.AppendBlock(42, 1000, block, false); err != nil {
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify the file is a valid journal readable by Reader.
	files, _ := filepath.Glob(filepath.Join(dir, "*.lpj"))
	if len(files) != 1 {
		t.Fatalf("expected 1 journal file, got %d", len(files))
	}

	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	r, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version() != journal.Version2 {
		t.Fatalf("expected v2, got %d", r.Version())
	}
	if r.BlockCount() != 1 {
		t.Fatalf("expected 1 block, got %d", r.BlockCount())
	}
	if !r.Next() {
		t.Fatal("expected at least one frame")
	}
	if r.FrameSeq() != 42 {
		t.Fatalf("expected seq 42, got %d", r.FrameSeq())
	}
}

func TestBlockWriter_MultipleBlocks(t *testing.T) {
	dir := t.TempDir()
	bw, err := NewBlockWriter(BlockWriterConfig{
		Dir:         dir,
		BlockSize:   4096,
		Compression: journal.CompressionNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		block := makeTestBlock(4096, int64(i)*1000000, uint64(i*100+1))
		if err := bw.AppendBlock(uint64(i*100+1), int64(i)*1000000, block, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.lpj"))
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	r, err := journal.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if r.BlockCount() != 5 {
		t.Fatalf("expected 5 blocks, got %d", r.BlockCount())
	}
}

func TestBlockWriter_Rotation(t *testing.T) {
	dir := t.TempDir()
	bw, err := NewBlockWriter(BlockWriterConfig{
		Dir:         dir,
		BlockSize:   4096,
		Compression: journal.CompressionNone,
		RotateSize:  int64(journal.FileHeaderSize + 4096), // rotate after 1 block
	})
	if err != nil {
		t.Fatal(err)
	}

	// Write 3 blocks, each should end up in its own file.
	for i := range 3 {
		baseTime := time.Now().Add(time.Duration(i) * time.Second).UnixMicro()
		block := makeTestBlock(4096, baseTime, uint64(i*100+1))
		if err := bw.AppendBlock(uint64(i*100+1), baseTime, block, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.lpj"))
	if len(files) < 2 {
		t.Fatalf("expected multiple files from rotation, got %d", len(files))
	}
}

func TestBlockWriter_InvalidBlockSize(t *testing.T) {
	dir := t.TempDir()
	_, err := NewBlockWriter(BlockWriterConfig{
		Dir:       dir,
		BlockSize: 1000, // not power of 2
	})
	if err == nil {
		t.Fatal("expected error for non-power-of-2 block size")
	}
}

func TestBlockWriter_CRCValidation(t *testing.T) {
	dir := t.TempDir()
	bw, err := NewBlockWriter(BlockWriterConfig{
		Dir:         dir,
		BlockSize:   4096,
		Compression: journal.CompressionNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Build a block with bad CRC
	block := makeTestBlock(4096, 1000, 1)
	block[0] = 0xFF // corrupt it

	err = bw.AppendBlock(1, 1000, block, false)
	if err == nil {
		t.Fatal("expected CRC error for corrupted block")
	}
}

// makeTestBlock builds a valid v2 uncompressed block with a single 8-byte frame.
func makeTestBlock(blockSize int, baseTimeUs int64, baseSeq uint64) []byte {
	block := make([]byte, blockSize)

	// v2 header: BaseTime(8) + BaseSeq(8)
	binary.LittleEndian.PutUint64(block[0:8], uint64(baseTimeUs))
	binary.LittleEndian.PutUint64(block[8:16], baseSeq)

	// One frame: delta=0 (varint 0x00), CAN ID with standard flag, 8 bytes data
	off := 16
	block[off] = 0x00 // delta varint = 0
	off++
	canID := uint32(0x80000000 | 0x09F80100) // standard flag + arbitrary CAN ID
	binary.LittleEndian.PutUint32(block[off:], canID)
	off += 4
	// 8 bytes of frame data
	for i := range 8 {
		block[off+i] = byte(i + 1)
	}

	// Device table: 0 entries (just the count)
	devTableBytes := 2
	devTableOff := blockSize - journal.BlockTrailerLen - devTableBytes
	binary.LittleEndian.PutUint16(block[devTableOff:], 0)

	// Trailer: DeviceTableSize(2) + FrameCount(4) + CRC(4)
	trailerOff := blockSize - journal.BlockTrailerLen
	binary.LittleEndian.PutUint16(block[trailerOff:], uint16(devTableBytes))
	binary.LittleEndian.PutUint32(block[trailerOff+2:], 1) // 1 frame
	checksum := crc32.Checksum(block[:blockSize-4], journal.CRC32cTable)
	binary.LittleEndian.PutUint32(block[blockSize-4:], checksum)

	return block
}
