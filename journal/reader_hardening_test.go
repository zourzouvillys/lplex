package journal

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBlockIndexRejectsExcessiveCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-index.lpj")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const count = maxBlockIndexEntries + 1
	tableSize := int64(count) * 8
	fileSize := int64(FileHeaderSize) + tableSize + 8
	if err := f.Truncate(fileSize); err != nil {
		t.Fatal(err)
	}

	var hdr [16]byte
	copy(hdr[0:3], Magic[:])
	hdr[3] = Version
	binary.LittleEndian.PutUint32(hdr[4:8], 4096)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(CompressionZstd))
	if _, err := f.WriteAt(hdr[:], 0); err != nil {
		t.Fatal(err)
	}

	var tail [8]byte
	binary.LittleEndian.PutUint32(tail[0:4], uint32(count))
	copy(tail[4:8], BlockIndexMagic[:])
	if _, err := f.WriteAt(tail[:], fileSize-8); err != nil {
		t.Fatal(err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()

	_, err = readBlockIndex(rf, fileSize)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected excessive count error, got %v", err)
	}
}

func TestInspectBlockRejectsOversizedCompressedLen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-compressed-len.lpj")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	block0Off := int64(FileHeaderSize)
	block1Off := block0Off + int64(BlockHeaderLen) + 1

	var fh [16]byte
	copy(fh[0:3], Magic[:])
	fh[3] = Version
	binary.LittleEndian.PutUint32(fh[4:8], 4096)
	binary.LittleEndian.PutUint32(fh[8:12], uint32(CompressionZstd))
	if _, err := f.WriteAt(fh[:], 0); err != nil {
		t.Fatal(err)
	}

	// Block 0 claims a payload far larger than space until block 1.
	b0 := make([]byte, BlockHeaderLen)
	binary.LittleEndian.PutUint64(b0[0:8], 1)
	binary.LittleEndian.PutUint32(b0[8:12], 9999)
	if _, err := f.WriteAt(b0[:], block0Off); err != nil {
		t.Fatal(err)
	}

	// Block 1 is just there to define the boundary for block 0.
	b1 := make([]byte, BlockHeaderLen)
	binary.LittleEndian.PutUint64(b1[0:8], 2)
	binary.LittleEndian.PutUint32(b1[8:12], 1)
	if _, err := f.WriteAt(b1[:], block1Off); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0x00}, block1Off+int64(BlockHeaderLen)); err != nil {
		t.Fatal(err)
	}

	indexStart := block1Off + int64(BlockHeaderLen) + 1
	index := make([]byte, 2*8+8)
	binary.LittleEndian.PutUint64(index[0:8], uint64(block0Off))
	binary.LittleEndian.PutUint64(index[8:16], uint64(block1Off))
	binary.LittleEndian.PutUint32(index[16:20], 2)
	copy(index[20:24], BlockIndexMagic[:])
	if _, err := f.WriteAt(index, indexStart); err != nil {
		t.Fatal(err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()

	r, err := NewReader(rf)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	_, err = r.InspectBlock(0)
	if err == nil || !strings.Contains(err.Error(), "invalid compressed length") {
		t.Fatalf("expected invalid compressed length error, got %v", err)
	}
}

func TestInspectBlockRejectsOversizedDictLen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-dict-len.lpj")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	block0Off := int64(FileHeaderSize)
	block1Off := block0Off + int64(BlockHeaderLenDict) + 2

	var fh [16]byte
	copy(fh[0:3], Magic[:])
	fh[3] = Version
	binary.LittleEndian.PutUint32(fh[4:8], 4096)
	binary.LittleEndian.PutUint32(fh[8:12], uint32(CompressionZstdDict))
	if _, err := f.WriteAt(fh[:], 0); err != nil {
		t.Fatal(err)
	}

	b0 := make([]byte, BlockHeaderLenDict)
	binary.LittleEndian.PutUint64(b0[0:8], 1)
	binary.LittleEndian.PutUint32(b0[8:12], 100)
	binary.LittleEndian.PutUint32(b0[12:16], 1)
	if _, err := f.WriteAt(b0[:], block0Off); err != nil {
		t.Fatal(err)
	}

	b1 := make([]byte, BlockHeaderLenDict)
	binary.LittleEndian.PutUint64(b1[0:8], 2)
	binary.LittleEndian.PutUint32(b1[8:12], 0)
	binary.LittleEndian.PutUint32(b1[12:16], 1)
	if _, err := f.WriteAt(b1[:], block1Off); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0x00, 0x00}, block1Off+int64(BlockHeaderLenDict)); err != nil {
		t.Fatal(err)
	}

	indexStart := block1Off + int64(BlockHeaderLenDict) + 2
	index := make([]byte, 2*8+8)
	binary.LittleEndian.PutUint64(index[0:8], uint64(block0Off))
	binary.LittleEndian.PutUint64(index[8:16], uint64(block1Off))
	binary.LittleEndian.PutUint32(index[16:20], 2)
	copy(index[20:24], BlockIndexMagic[:])
	if _, err := f.WriteAt(index, indexStart); err != nil {
		t.Fatal(err)
	}

	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()

	r, err := NewReader(rf)
	if err != nil {
		t.Fatalf("new reader: %v", err)
	}

	_, err = r.InspectBlock(0)
	if err == nil || !strings.Contains(err.Error(), "invalid dictionary length") {
		t.Fatalf("expected invalid dictionary length error, got %v", err)
	}
}
