package lplex

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/dict"
	"github.com/klauspost/compress/zstd"
	"github.com/sixfathoms/lplex/journal"
)

// JournalConfig configures the journal writer.
type JournalConfig struct {
	Dir            string
	Prefix         string                  // default: "nmea2k"
	BlockSize      int                     // default: 262144, power of 2, min 4096
	Compression    journal.CompressionType // default: CompressionNone
	RotateDuration time.Duration           // 0 = no limit
	RotateSize     int64                   // 0 = no limit
	RotateCount    int64                   // 0 = no limit
	Logger         *slog.Logger
}

func (c *JournalConfig) setDefaults() {
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

func (c *JournalConfig) validate() error {
	if c.BlockSize < 4096 {
		return fmt.Errorf("block size %d too small, minimum 4096", c.BlockSize)
	}
	if c.BlockSize&(c.BlockSize-1) != 0 {
		return fmt.Errorf("block size %d must be a power of 2", c.BlockSize)
	}
	return nil
}

// journalDeviceChange records an address claim seen during a block.
type journalDeviceChange struct {
	Source   uint8
	NAME     uint64
	FrameIdx uint32
}

// JournalWriter writes CAN frames to block-based journal files.
type JournalWriter struct {
	cfg     JournalConfig
	devices *DeviceRegistry
	ch      <-chan RxFrame

	// current file
	file       *os.File
	fileStart  time.Time
	fileBytes  int64
	fileFrames int64

	// current block (buffered in memory)
	block      []byte
	dataOffset int    // write cursor for frame data (starts at 8)
	frameCount uint32
	baseTimeUs int64 // first frame's absolute time (unix microseconds)
	lastTimeUs int64 // for delta encoding

	// device tracking for current block
	blockStartDevices         []journal.Device
	blockStartDeviceTableSize int // exact serialized size of block-start entries (cached)
	blockDeviceChanges        []journalDeviceChange

	// compression state
	zEncoder     *zstd.Encoder
	compressBuf  []byte
	blockOffsets []int64 // file offsets of each block (for block index)
}

// NewJournalWriter creates a writer. Call Run to start.
func NewJournalWriter(cfg JournalConfig, devices *DeviceRegistry, ch <-chan RxFrame) (*JournalWriter, error) {
	cfg.setDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	w := &JournalWriter{
		cfg:     cfg,
		devices: devices,
		ch:      ch,
		block:   make([]byte, cfg.BlockSize),
	}

	switch cfg.Compression {
	case journal.CompressionZstd:
		enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			return nil, fmt.Errorf("init zstd encoder: %w", err)
		}
		w.zEncoder = enc
		w.compressBuf = make([]byte, 0, cfg.BlockSize)
	case journal.CompressionZstdDict:
		// No pre-created encoder: we build one per block with the trained dictionary.
		w.compressBuf = make([]byte, 0, cfg.BlockSize)
	}

	return w, nil
}

// Run is the main loop. Blocks until ctx is cancelled or the channel is closed.
func (w *JournalWriter) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return w.finalize()
		case frame, ok := <-w.ch:
			if !ok {
				return w.finalize()
			}
			if err := w.appendFrame(frame); err != nil {
				w.cfg.Logger.Error("journal write error", "error", err)
				return err
			}
		}
	}
}

// finalize flushes any pending block, writes the block index, and closes the file.
func (w *JournalWriter) finalize() error {
	if w.frameCount > 0 {
		if err := w.flushBlock(); err != nil {
			return err
		}
	}
	if w.file != nil {
		if w.cfg.Compression != journal.CompressionNone {
			if err := w.writeBlockIndex(); err != nil {
				return err
			}
		}
		if err := w.file.Sync(); err != nil {
			return err
		}
		return w.file.Close()
	}
	return nil
}

// appendFrame encodes a frame into the current block, flushing if needed.
func (w *JournalWriter) appendFrame(frame RxFrame) error {
	if w.file == nil {
		if err := w.openFile(frame.Timestamp); err != nil {
			return err
		}
	}

	tsUs := frame.Timestamp.UnixMicro()
	canID := BuildCANID(frame.Header)
	dataLen := len(frame.Data)
	standard := dataLen == 8

	// Calculate encoded size
	var deltaUs uint64
	if w.frameCount > 0 {
		if tsUs >= w.lastTimeUs {
			deltaUs = uint64(tsUs - w.lastTimeUs)
		}
	}
	deltaSize := journal.UvarintSize(deltaUs)

	var size int
	if standard {
		size = deltaSize + 4 + 8
	} else {
		size = deltaSize + 4 + journal.UvarintSize(uint64(dataLen)) + dataLen
	}

	// Check if frame fits in current block (reserve space for device table + trailer)
	devTableSize := w.deviceTableSize()
	available := w.cfg.BlockSize - w.dataOffset - devTableSize - journal.BlockTrailerLen
	if available < size {
		if err := w.flushBlock(); err != nil {
			return err
		}
		if err := w.checkRotation(); err != nil {
			return err
		}
		w.initBlock(tsUs)
		deltaUs = 0
	}

	if w.frameCount == 0 {
		w.initBlock(tsUs)
	}

	// Encode into block buffer
	off := w.dataOffset

	// Delta varint
	off += binary.PutUvarint(w.block[off:], deltaUs)

	// CAN ID with standard-length flag
	storedID := canID
	if standard {
		storedID |= 0x80000000
	}
	binary.LittleEndian.PutUint32(w.block[off:], storedID)
	off += 4

	if standard {
		copy(w.block[off:], frame.Data)
		off += 8
	} else {
		off += binary.PutUvarint(w.block[off:], uint64(dataLen))
		copy(w.block[off:], frame.Data)
		off += dataLen
	}

	// Track address claims for device table
	if frame.Header.PGN == 60928 && len(frame.Data) >= 8 {
		name := binary.LittleEndian.Uint64(frame.Data[0:8])
		w.blockDeviceChanges = append(w.blockDeviceChanges, journalDeviceChange{
			Source:   frame.Header.Source,
			NAME:     name,
			FrameIdx: w.frameCount,
		})
	}

	w.dataOffset = off
	w.lastTimeUs = tsUs
	w.frameCount++
	w.fileFrames++

	return nil
}

// initBlock sets up a fresh block at the given base time.
func (w *JournalWriter) initBlock(baseTimeUs int64) {
	clear(w.block)
	w.dataOffset = 8
	w.frameCount = 0
	w.baseTimeUs = baseTimeUs
	w.lastTimeUs = baseTimeUs
	binary.LittleEndian.PutUint64(w.block[0:8], uint64(baseTimeUs))

	w.blockStartDevices = w.blockStartDevices[:0]
	tableSize := 0
	for _, dev := range w.devices.Snapshot() {
		if dev.NAME != 0 {
			jd := journal.Device{
				Source:          dev.Source,
				NAME:            dev.NAME,
				ProductCode:     dev.ProductCode,
				ModelID:         dev.ModelID,
				SoftwareVersion: dev.SoftwareVersion,
				ModelVersion:    dev.ModelVersion,
				ModelSerial:     dev.ModelSerial,
			}
			w.blockStartDevices = append(w.blockStartDevices, jd)
			// Fixed: Source(1) + NAME(8) + ActiveFrom(4) + ProductCode(2) = 15
			// Variable: 4 length bytes + string data
			tableSize += 15 + 4 + len(jd.ModelID) + len(jd.SoftwareVersion) + len(jd.ModelVersion) + len(jd.ModelSerial)
		}
	}
	w.blockStartDeviceTableSize = tableSize
	w.blockDeviceChanges = w.blockDeviceChanges[:0]
}

// flushBlock finalizes the current block and writes it to disk.
func (w *JournalWriter) flushBlock() error {
	if w.frameCount == 0 {
		return nil
	}

	bs := w.cfg.BlockSize

	type devEntry struct {
		Source          uint8
		NAME            uint64
		ActiveFrom      uint32
		ProductCode     uint16
		ModelID         string
		SoftwareVersion string
		ModelVersion    string
		ModelSerial     string
	}

	entryCount := len(w.blockStartDevices) + len(w.blockDeviceChanges)
	entries := make([]devEntry, 0, entryCount)

	for _, d := range w.blockStartDevices {
		entries = append(entries, devEntry{
			Source:          d.Source,
			NAME:            d.NAME,
			ActiveFrom:      0,
			ProductCode:     d.ProductCode,
			ModelID:         d.ModelID,
			SoftwareVersion: d.SoftwareVersion,
			ModelVersion:    d.ModelVersion,
			ModelSerial:     d.ModelSerial,
		})
	}
	for _, c := range w.blockDeviceChanges {
		e := devEntry{Source: c.Source, NAME: c.NAME, ActiveFrom: c.FrameIdx}
		if dev := w.devices.Get(c.Source); dev != nil {
			e.ProductCode = dev.ProductCode
			e.ModelID = dev.ModelID
			e.SoftwareVersion = dev.SoftwareVersion
			e.ModelVersion = dev.ModelVersion
			e.ModelSerial = dev.ModelSerial
		}
		entries = append(entries, e)
	}

	// Calculate exact device table size.
	devTableBytes := 2
	for _, e := range entries {
		devTableBytes += 15 + 4 + len(e.ModelID) + len(e.SoftwareVersion) + len(e.ModelVersion) + len(e.ModelSerial)
	}
	devTableOffset := bs - journal.BlockTrailerLen - devTableBytes

	off := devTableOffset
	binary.LittleEndian.PutUint16(w.block[off:], uint16(len(entries)))
	off += 2
	for _, e := range entries {
		w.block[off] = e.Source
		off++
		binary.LittleEndian.PutUint64(w.block[off:], e.NAME)
		off += 8
		binary.LittleEndian.PutUint32(w.block[off:], e.ActiveFrom)
		off += 4
		binary.LittleEndian.PutUint16(w.block[off:], e.ProductCode)
		off += 2
		off += journal.PutLenPrefixedString(w.block[off:], e.ModelID)
		off += journal.PutLenPrefixedString(w.block[off:], e.SoftwareVersion)
		off += journal.PutLenPrefixedString(w.block[off:], e.ModelVersion)
		off += journal.PutLenPrefixedString(w.block[off:], e.ModelSerial)
	}

	trailerOff := bs - journal.BlockTrailerLen
	binary.LittleEndian.PutUint16(w.block[trailerOff:], uint16(devTableBytes))
	binary.LittleEndian.PutUint32(w.block[trailerOff+2:], w.frameCount)
	checksum := crc32.Checksum(w.block[:bs-4], journal.CRC32cTable)
	binary.LittleEndian.PutUint32(w.block[bs-4:], checksum)

	switch w.cfg.Compression {
	case journal.CompressionZstd:
		return w.writeCompressedBlock()
	case journal.CompressionZstdDict:
		return w.writeDictCompressedBlock()
	}

	n, err := w.file.Write(w.block)
	if err != nil {
		return fmt.Errorf("journal block write: %w", err)
	}
	w.fileBytes += int64(n)

	w.frameCount = 0
	w.dataOffset = 8

	return nil
}

// writeCompressedBlock writes a compressed block with its 12-byte header.
func (w *JournalWriter) writeCompressedBlock() error {
	// Record the file offset before writing
	w.blockOffsets = append(w.blockOffsets, w.fileBytes)

	// Compress the full uncompressed block
	w.compressBuf = w.zEncoder.EncodeAll(w.block, w.compressBuf[:0])

	// Write 12-byte header: BaseTime(8) + CompressedLen(4)
	var hdr [12]byte
	binary.LittleEndian.PutUint64(hdr[0:8], uint64(w.baseTimeUs))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(w.compressBuf)))

	n, err := w.file.Write(hdr[:])
	if err != nil {
		return fmt.Errorf("journal compressed block header write: %w", err)
	}
	w.fileBytes += int64(n)

	n, err = w.file.Write(w.compressBuf)
	if err != nil {
		return fmt.Errorf("journal compressed block data write: %w", err)
	}
	w.fileBytes += int64(n)

	w.frameCount = 0
	w.dataOffset = 8

	return nil
}

// writeDictCompressedBlock trains a per-block zstd dictionary and writes
// the 16-byte header + dictionary + compressed payload. Falls back to
// DictLen=0 (plain zstd) when dictionary training fails or isn't worthwhile.
func (w *JournalWriter) writeDictCompressedBlock() error {
	w.blockOffsets = append(w.blockOffsets, w.fileBytes)

	var dictBytes []byte

	// Only attempt dictionary training when we have enough frame data
	// to produce meaningful samples (at least 1KB of actual content).
	if w.dataOffset > 1024 {
		dictBytes = w.buildBlockDict()
	}

	if dictBytes != nil {
		// Compress with the trained dictionary.
		enc, err := zstd.NewWriter(nil, zstd.WithEncoderDict(dictBytes), zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			w.cfg.Logger.Warn("dict encoder init failed, falling back to plain zstd", "error", err, "dict_len", len(dictBytes))
			dictBytes = nil
		} else {
			w.compressBuf = enc.EncodeAll(w.block, w.compressBuf[:0])
			_ = enc.Close()
		}
	}

	if dictBytes == nil {
		// Fallback: compress without dictionary (DictLen=0 in the header).
		if w.zEncoder == nil {
			enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
			if err != nil {
				return fmt.Errorf("journal zstd encoder init: %w", err)
			}
			w.zEncoder = enc
		}
		w.compressBuf = w.zEncoder.EncodeAll(w.block, w.compressBuf[:0])
	}

	// Write 16-byte header: BaseTime(8) + DictLen(4) + CompressedLen(4)
	var hdr [16]byte
	binary.LittleEndian.PutUint64(hdr[0:8], uint64(w.baseTimeUs))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(dictBytes)))
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(w.compressBuf)))

	n, err := w.file.Write(hdr[:])
	if err != nil {
		return fmt.Errorf("journal dict block header write: %w", err)
	}
	w.fileBytes += int64(n)

	if len(dictBytes) > 0 {
		n, err = w.file.Write(dictBytes)
		if err != nil {
			return fmt.Errorf("journal dict block dict write: %w", err)
		}
		w.fileBytes += int64(n)
	}

	n, err = w.file.Write(w.compressBuf)
	if err != nil {
		return fmt.Errorf("journal dict block data write: %w", err)
	}
	w.fileBytes += int64(n)

	w.frameCount = 0
	w.dataOffset = 8

	return nil
}

// buildBlockDict trains a zstd dictionary from the current block's data.
// Returns nil if training fails.
func (w *JournalWriter) buildBlockDict() []byte {
	// Split the frame data region into ~256-byte samples for dictionary training.
	const sampleSize = 256
	frameEnd := w.dataOffset
	var samples [][]byte
	for off := 8; off+sampleSize <= frameEnd; off += sampleSize / 2 {
		s := make([]byte, sampleSize)
		copy(s, w.block[off:off+sampleSize])
		samples = append(samples, s)
	}
	if len(samples) < 4 {
		return nil
	}

	d, err := dict.BuildZstdDict(samples, dict.Options{
		MaxDictSize: 8192,
		HashBytes:   6,
		ZstdDictID:  1,
		ZstdLevel:   zstd.SpeedDefault,
	})
	if err != nil {
		w.cfg.Logger.Warn("dict build failed", "error", err, "samples", len(samples))
		return nil
	}
	return d
}

// writeBlockIndex appends the block index to the end of the file.
func (w *JournalWriter) writeBlockIndex() error {
	if len(w.blockOffsets) == 0 {
		return nil
	}

	// Write offset table
	buf := make([]byte, len(w.blockOffsets)*8+8)
	for i, off := range w.blockOffsets {
		binary.LittleEndian.PutUint64(buf[i*8:], uint64(off))
	}

	// Write count + magic
	tail := buf[len(w.blockOffsets)*8:]
	binary.LittleEndian.PutUint32(tail[0:4], uint32(len(w.blockOffsets)))
	copy(tail[4:8], journal.BlockIndexMagic[:])

	if _, err := w.file.Write(buf); err != nil {
		return fmt.Errorf("journal block index write: %w", err)
	}

	return nil
}

// checkRotation checks if rotation is needed and performs it.
func (w *JournalWriter) checkRotation() error {
	rotate := (w.cfg.RotateDuration > 0 && time.Since(w.fileStart) >= w.cfg.RotateDuration) ||
		(w.cfg.RotateSize > 0 && w.fileBytes >= w.cfg.RotateSize) ||
		(w.cfg.RotateCount > 0 && w.fileFrames >= w.cfg.RotateCount)
	if !rotate {
		return nil
	}

	if w.cfg.Compression != journal.CompressionNone {
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
	w.fileFrames = 0
	w.blockOffsets = w.blockOffsets[:0]
	return nil
}

// openFile creates a new journal file and writes the file header.
func (w *JournalWriter) openFile(ts time.Time) error {
	if err := os.MkdirAll(w.cfg.Dir, 0o755); err != nil {
		return fmt.Errorf("journal mkdir: %w", err)
	}

	name := fmt.Sprintf("%s-%s.lpj", w.cfg.Prefix, ts.UTC().Format("20060102T150405.000Z"))
	path := filepath.Join(w.cfg.Dir, name)

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("journal create: %w", err)
	}

	var hdr [16]byte
	copy(hdr[0:3], journal.Magic[:])
	hdr[3] = journal.Version
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(w.cfg.BlockSize))
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(w.cfg.Compression))

	if _, err := f.Write(hdr[:]); err != nil {
		_ = f.Close()
		return fmt.Errorf("journal header write: %w", err)
	}

	w.file = f
	w.fileStart = ts
	w.fileBytes = int64(journal.FileHeaderSize)
	w.fileFrames = 0
	w.blockOffsets = w.blockOffsets[:0]

	w.cfg.Logger.Info("journal file opened", "path", path)
	return nil
}

// deviceTableSize returns the maximum device table size for space reservation.
// Block-start entries use exact cached size, in-block changes use worst-case.
func (w *JournalWriter) deviceTableSize() int {
	return 2 + w.blockStartDeviceTableSize + len(w.blockDeviceChanges)*journal.DeviceEntryMaxSize
}
