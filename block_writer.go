package lplex

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sixfathoms/lplex/journal"
)

// BlockWriterConfig configures a BlockWriter.
type BlockWriterConfig struct {
	Dir            string
	Prefix         string // default: "nmea2k"
	BlockSize      int    // uncompressed block size (from source journal)
	Compression    journal.CompressionType
	RotateDuration time.Duration // 0 = no limit
	RotateSize     int64         // 0 = no limit
	Logger         *slog.Logger
}

func (c *BlockWriterConfig) setDefaults() {
	if c.Prefix == "" {
		c.Prefix = "nmea2k"
	}
	if c.BlockSize == 0 {
		c.BlockSize = 262144
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// BlockWriter appends pre-encoded journal blocks to .lpj files. Unlike
// JournalWriter, it receives blocks that are already serialized (with CRC,
// device table, frame data). Used by the cloud replication server to write
// backfill blocks byte-for-byte without decompression or re-encoding.
type BlockWriter struct {
	cfg BlockWriterConfig

	file       *os.File
	fileStart  time.Time
	fileBytes  int64
	fileBlocks int

	blockOffsets []int64 // file offsets of each block (for compressed block index)
}

// NewBlockWriter creates a new BlockWriter. Call AppendBlock to write blocks.
// Call Close when done to finalize the current file.
func NewBlockWriter(cfg BlockWriterConfig) (*BlockWriter, error) {
	cfg.setDefaults()
	if cfg.BlockSize < 4096 || cfg.BlockSize&(cfg.BlockSize-1) != 0 {
		return nil, fmt.Errorf("block size %d must be a power of 2 >= 4096", cfg.BlockSize)
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("block writer mkdir: %w", err)
	}

	return &BlockWriter{cfg: cfg}, nil
}

// AppendBlock writes a pre-encoded block to the current journal file.
// For compressed blocks, the data is the compressed payload (written with a
// 20-byte v2 header). For uncompressed blocks, data is the full block bytes
// (must be exactly BlockSize with valid CRC).
func (w *BlockWriter) AppendBlock(baseSeq uint64, baseTimeUs int64, data []byte, compressed bool) error {
	if w.file == nil {
		ts := time.UnixMicro(baseTimeUs)
		if err := w.openFile(ts); err != nil {
			return err
		}
	}

	if compressed {
		return w.writeCompressedBlock(baseSeq, baseTimeUs, data)
	}
	return w.writeUncompressedBlock(data)
}

// writeUncompressedBlock writes a full fixed-size block. Validates CRC before writing.
func (w *BlockWriter) writeUncompressedBlock(data []byte) error {
	if len(data) != w.cfg.BlockSize {
		return fmt.Errorf("uncompressed block size %d != expected %d", len(data), w.cfg.BlockSize)
	}

	// Validate CRC
	bs := w.cfg.BlockSize
	stored := binary.LittleEndian.Uint32(data[bs-4:])
	computed := crc32.Checksum(data[:bs-4], journal.CRC32cTable)
	if stored != computed {
		return fmt.Errorf("block CRC mismatch: stored=%08x computed=%08x", stored, computed)
	}

	n, err := w.file.Write(data)
	if err != nil {
		return fmt.Errorf("block write: %w", err)
	}
	w.fileBytes += int64(n)
	w.fileBlocks++

	return w.checkRotation()
}

// writeCompressedBlock writes a compressed block with a 20-byte v2 header:
// BaseTime(8) + BaseSeq(8) + CompressedLen(4) + compressed data.
func (w *BlockWriter) writeCompressedBlock(baseSeq uint64, baseTimeUs int64, data []byte) error {
	w.blockOffsets = append(w.blockOffsets, w.fileBytes)

	var hdr [20]byte
	binary.LittleEndian.PutUint64(hdr[0:8], uint64(baseTimeUs))
	binary.LittleEndian.PutUint64(hdr[8:16], baseSeq)
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(data)))

	n, err := w.file.Write(hdr[:])
	if err != nil {
		return fmt.Errorf("compressed block header write: %w", err)
	}
	w.fileBytes += int64(n)

	n, err = w.file.Write(data)
	if err != nil {
		return fmt.Errorf("compressed block data write: %w", err)
	}
	w.fileBytes += int64(n)
	w.fileBlocks++

	return w.checkRotation()
}

// Close finalizes the current file (writes block index for compressed files,
// syncs, and closes). Safe to call multiple times or on a writer with no open file.
func (w *BlockWriter) Close() error {
	if w.file == nil {
		return nil
	}

	if w.cfg.Compression != journal.CompressionNone && len(w.blockOffsets) > 0 {
		if err := w.writeBlockIndex(); err != nil {
			return err
		}
	}

	if err := w.file.Sync(); err != nil {
		return err
	}
	err := w.file.Close()
	w.file = nil
	w.fileBytes = 0
	w.fileBlocks = 0
	w.blockOffsets = w.blockOffsets[:0]
	return err
}

func (w *BlockWriter) openFile(ts time.Time) error {
	name := fmt.Sprintf("%s-%s.lpj", w.cfg.Prefix, ts.UTC().Format("20060102T150405.000Z"))
	path := filepath.Join(w.cfg.Dir, name)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("block writer create: %w", err)
	}

	var hdr [16]byte
	copy(hdr[0:3], journal.Magic[:])
	hdr[3] = journal.Version2
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(w.cfg.BlockSize))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(w.cfg.Compression))

	if _, err := f.Write(hdr[:]); err != nil {
		_ = f.Close()
		return fmt.Errorf("block writer header write: %w", err)
	}

	w.file = f
	w.fileStart = ts
	w.fileBytes = int64(journal.FileHeaderSize)
	w.fileBlocks = 0
	w.blockOffsets = w.blockOffsets[:0]

	w.cfg.Logger.Info("journal file opened (block writer)", "path", path)
	return nil
}

func (w *BlockWriter) checkRotation() error {
	rotate := (w.cfg.RotateDuration > 0 && time.Since(w.fileStart) >= w.cfg.RotateDuration) ||
		(w.cfg.RotateSize > 0 && w.fileBytes >= w.cfg.RotateSize)
	if !rotate {
		return nil
	}

	return w.rotateFile()
}

func (w *BlockWriter) rotateFile() error {
	if w.cfg.Compression != journal.CompressionNone && len(w.blockOffsets) > 0 {
		if err := w.writeBlockIndex(); err != nil {
			return err
		}
	}

	if err := w.file.Sync(); err != nil {
		return err
	}
	if err := w.file.Close(); err != nil {
		return err
	}
	w.file = nil
	w.fileBytes = 0
	w.fileBlocks = 0
	w.blockOffsets = w.blockOffsets[:0]
	return nil
}

func (w *BlockWriter) writeBlockIndex() error {
	if len(w.blockOffsets) == 0 {
		return nil
	}

	buf := make([]byte, len(w.blockOffsets)*8+8)
	for i, off := range w.blockOffsets {
		binary.LittleEndian.PutUint64(buf[i*8:], uint64(off))
	}

	tail := buf[len(w.blockOffsets)*8:]
	binary.LittleEndian.PutUint32(tail[0:4], uint32(len(w.blockOffsets)))
	copy(tail[4:8], journal.BlockIndexMagic[:])

	if _, err := w.file.Write(buf); err != nil {
		return fmt.Errorf("block index write: %w", err)
	}

	return nil
}
