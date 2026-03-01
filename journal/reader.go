package journal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/sixfathoms/lplex/canbus"
)

const maxBlockIndexEntries = 1 << 18 // 262,144 blocks (~2 MiB index table)

// Entry is a single decoded frame from the journal.
type Entry struct {
	Timestamp time.Time
	Header    canbus.CANHeader
	Data      []byte
}

// blockInfo stores the file offset, base time, and base seq for a block.
type blockInfo struct {
	Offset   int64
	BaseTime int64
	BaseSeq  uint64 // v2 only; 0 for v1
}

// Reader reads frames from a block-based journal file.
type Reader struct {
	r           io.ReadSeeker
	blockSize   int
	blockBuf    []byte
	compression CompressionType
	version     byte // 0x01 or 0x02

	// block index (built on open from index footer or forward scan)
	blocks []blockInfo

	// current block state
	currentBlock int // -1 = before first block
	blockData    []byte
	blockOff     int // read cursor within block frame data area
	frameIdx     int // frame index within block
	frameCount   int
	baseTimeUs   int64
	lastTimeUs   int64
	devTableOff  int
	baseSeqBlock uint64 // v2 only: base seq for current loaded block

	// current frame (valid after Next returns true)
	entry Entry
	err   error

	// zstd decoder, lazy-init on first compressed block
	zDecoder *zstd.Decoder
}

// NewReader opens a journal for reading. Validates the file header.
func NewReader(r io.ReadSeeker) (*Reader, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("read journal header: %w", err)
	}
	if hdr[0] != Magic[0] || hdr[1] != Magic[1] || hdr[2] != Magic[2] {
		return nil, fmt.Errorf("not a journal file (bad magic)")
	}
	version := hdr[3]
	if version != Version && version != Version2 {
		return nil, fmt.Errorf("unsupported journal version %d", version)
	}
	blockSize := int(binary.LittleEndian.Uint32(hdr[4:8]))
	if blockSize < 4096 || blockSize&(blockSize-1) != 0 {
		return nil, fmt.Errorf("invalid block size %d", blockSize)
	}

	flags := binary.LittleEndian.Uint32(hdr[8:12])
	compression := CompressionType(flags & 0xFF)
	if compression > CompressionZstdDict {
		return nil, fmt.Errorf("unsupported compression type %d", compression)
	}

	end, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("seek to end: %w", err)
	}

	jr := &Reader{
		r:            r,
		blockSize:    blockSize,
		blockBuf:     make([]byte, blockSize),
		compression:  compression,
		version:      version,
		currentBlock: -1,
	}

	if compression == CompressionNone {
		// Uncompressed: fixed-size blocks, simple arithmetic.
		dataBytes := end - int64(FileHeaderSize)
		count := int(dataBytes / int64(blockSize))
		jr.blocks = make([]blockInfo, count)
		for i := range count {
			jr.blocks[i] = blockInfo{
				Offset: int64(FileHeaderSize) + int64(i)*int64(blockSize),
			}
		}
		// Populate base times lazily during SeekToTime (read on demand).
	} else {
		// Compressed: try block index, fall back to forward scan.
		blocks, indexErr := readBlockIndex(r, end, version)
		if indexErr != nil {
			blocks, err = scanBlocks(r, end, compression, version)
			if err != nil {
				return nil, fmt.Errorf("scan compressed blocks: %w", err)
			}
		}
		jr.blocks = blocks
	}

	return jr, nil
}

// readBlockIndex tries to read the block index from the end of the file.
func readBlockIndex(r io.ReadSeeker, fileSize int64, version byte) ([]blockInfo, error) {
	// Need at least 8 bytes for Count(4) + Magic(4)
	if fileSize < int64(FileHeaderSize)+8 {
		return nil, fmt.Errorf("file too small for block index")
	}

	// Read the last 8 bytes: Count + Magic
	if _, err := r.Seek(fileSize-8, io.SeekStart); err != nil {
		return nil, err
	}
	var tail [8]byte
	if _, err := io.ReadFull(r, tail[:]); err != nil {
		return nil, err
	}

	count := binary.LittleEndian.Uint32(tail[0:4])
	if tail[4] != BlockIndexMagic[0] || tail[5] != BlockIndexMagic[1] ||
		tail[6] != BlockIndexMagic[2] || tail[7] != BlockIndexMagic[3] {
		return nil, fmt.Errorf("no block index magic")
	}

	if count == 0 {
		return nil, nil
	}
	if count > maxBlockIndexEntries {
		return nil, fmt.Errorf("block index has %d entries, exceeds limit %d", count, maxBlockIndexEntries)
	}

	// Read the offset table: Count * 8 bytes before the tail
	tableSize := int64(count) * 8
	tableStart := fileSize - 8 - tableSize
	if tableStart < int64(FileHeaderSize) {
		return nil, fmt.Errorf("block index table extends before file header")
	}

	if _, err := r.Seek(tableStart, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, tableSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	blocks := make([]blockInfo, count)
	for i := range count {
		off := int64(binary.LittleEndian.Uint64(buf[i*8:]))
		if off < int64(FileHeaderSize) {
			return nil, fmt.Errorf("block %d offset %d before file header", i, off)
		}
		if off >= tableStart {
			return nil, fmt.Errorf("block %d offset %d overlaps index footer", i, off)
		}
		if i > 0 && off <= blocks[i-1].Offset {
			return nil, fmt.Errorf("block offsets not strictly increasing at %d", i)
		}
		blocks[i].Offset = off
	}

	// Read base times (and base seqs for v2) from each block header.
	hdrReadLen := 8 // v1: just BaseTime
	if version == Version2 {
		hdrReadLen = 16 // v2: BaseTime + BaseSeq
	}
	hdrBuf := make([]byte, hdrReadLen)
	for i := range blocks {
		if _, err := r.Seek(blocks[i].Offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek to block %d header: %w", i, err)
		}
		if _, err := io.ReadFull(r, hdrBuf); err != nil {
			return nil, fmt.Errorf("read block %d header: %w", i, err)
		}
		blocks[i].BaseTime = int64(binary.LittleEndian.Uint64(hdrBuf[0:8]))
		if version == Version2 {
			blocks[i].BaseSeq = binary.LittleEndian.Uint64(hdrBuf[8:16])
		}
	}

	return blocks, nil
}

// scanBlocks forward-scans compressed blocks to build the block info table.
func scanBlocks(r io.ReadSeeker, fileSize int64, compression CompressionType, version byte) ([]blockInfo, error) {
	if _, err := r.Seek(int64(FileHeaderSize), io.SeekStart); err != nil {
		return nil, err
	}

	// v2 adds 8 bytes (BaseSeq) after BaseTime in compressed headers
	sfl := 0
	if version == Version2 {
		sfl = 8
	}

	hdrLen := BlockHeaderLen + sfl
	if compression == CompressionZstdDict {
		hdrLen = BlockHeaderLenDict + sfl
	}

	var blocks []blockInfo
	pos := int64(FileHeaderSize)

	hdr := make([]byte, hdrLen)
	for pos+int64(hdrLen) <= fileSize {
		if _, err := r.Seek(pos, io.SeekStart); err != nil {
			break
		}
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			break
		}
		baseTime := int64(binary.LittleEndian.Uint64(hdr[0:8]))
		var baseSeq uint64
		if version == Version2 {
			baseSeq = binary.LittleEndian.Uint64(hdr[8:16])
		}

		var blockEnd int64
		if compression == CompressionZstdDict {
			dictLen := binary.LittleEndian.Uint32(hdr[8+sfl : 12+sfl])
			compressedLen := binary.LittleEndian.Uint32(hdr[12+sfl : 16+sfl])
			if dictLen == 0 && compressedLen == 0 {
				break
			}
			blockEnd = pos + int64(hdrLen) + int64(dictLen) + int64(compressedLen)
		} else {
			compressedLen := binary.LittleEndian.Uint32(hdr[8+sfl : 12+sfl])
			if compressedLen == 0 {
				break
			}
			blockEnd = pos + int64(hdrLen) + int64(compressedLen)
		}

		if blockEnd > fileSize {
			break
		}

		blocks = append(blocks, blockInfo{
			Offset:   pos,
			BaseTime: baseTime,
			BaseSeq:  baseSeq,
		})
		pos = blockEnd
	}

	return blocks, nil
}

// BlockInfo holds metadata about a single block for inspection.
type BlockInfo struct {
	Index         int
	Offset        int64 // file offset
	BaseTime      time.Time
	FrameCount    int
	DeviceCount   int
	CompressedLen int // 0 for uncompressed blocks
	DictLen       int // 0 unless CompressionZstdDict
}

// Compression returns the compression type used by this journal file.
func (jr *Reader) Compression() CompressionType {
	return jr.compression
}

// BlockSize returns the uncompressed block size.
func (jr *Reader) BlockSize() int {
	return jr.blockSize
}

// BlockCount returns the number of complete blocks in the file.
func (jr *Reader) BlockCount() int {
	return len(jr.blocks)
}

// Version returns the journal format version (0x01 or 0x02).
func (jr *Reader) Version() byte {
	return jr.version
}

// FrameSeq returns the sequence number of the current frame.
// Only valid after Next returns true. Returns 0 for v1 files (no seq info).
func (jr *Reader) FrameSeq() uint64 {
	if jr.version != Version2 || jr.frameIdx == 0 {
		return 0
	}
	return jr.baseSeqBlock + uint64(jr.frameIdx-1)
}

// SeekToSeq positions the reader at the block containing the given sequence
// number via binary search on BaseSeq. Only works for v2 files.
// Returns an error for v1 files or if the seq is not found.
func (jr *Reader) SeekToSeq(seq uint64) error {
	if jr.version != Version2 {
		return fmt.Errorf("seq seeking requires journal v2")
	}
	if len(jr.blocks) == 0 {
		return fmt.Errorf("empty journal")
	}

	if jr.compression != CompressionNone {
		// Compressed: base seqs are already in memory.
		idx := sort.Search(len(jr.blocks), func(i int) bool {
			return jr.blocks[i].BaseSeq > seq
		})
		if idx > 0 {
			idx--
		}
		return jr.loadBlock(idx)
	}

	// Uncompressed: read BaseSeq from disk (O(log n) seeks).
	var seqBuf [8]byte
	lo, hi := 0, len(jr.blocks)-1
	result := 0
	for lo <= hi {
		mid := lo + (hi-lo)/2
		// BaseSeq is at offset 8 within the block (after BaseTime)
		if _, err := jr.r.Seek(jr.blocks[mid].Offset+8, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.ReadFull(jr.r, seqBuf[:]); err != nil {
			return err
		}
		baseSeq := binary.LittleEndian.Uint64(seqBuf[:])
		if baseSeq <= seq {
			result = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}

	return jr.loadBlock(result)
}

// seqFieldLen returns 8 for v2 (BaseSeq field present in compressed headers)
// or 0 for v1.
func (jr *Reader) seqFieldLen() int {
	if jr.version == Version2 {
		return 8
	}
	return 0
}

// InspectBlock loads block n and returns its metadata without iterating frames.
func (jr *Reader) InspectBlock(n int) (BlockInfo, error) {
	if n < 0 || n >= len(jr.blocks) {
		return BlockInfo{}, fmt.Errorf("block %d out of range [0, %d)", n, len(jr.blocks))
	}

	bi := BlockInfo{
		Index:  n,
		Offset: jr.blocks[n].Offset,
	}

	// For compressed blocks, read the header to get CompressedLen (and DictLen).
	sfl := jr.seqFieldLen()
	if jr.compression == CompressionZstdDict {
		if _, err := jr.r.Seek(jr.blocks[n].Offset, io.SeekStart); err != nil {
			return bi, err
		}
		hdr := make([]byte, BlockHeaderLenDict+sfl)
		if _, err := io.ReadFull(jr.r, hdr); err != nil {
			return bi, err
		}
		bi.DictLen = int(binary.LittleEndian.Uint32(hdr[8+sfl : 12+sfl]))
		bi.CompressedLen = int(binary.LittleEndian.Uint32(hdr[12+sfl : 16+sfl]))
	} else if jr.compression != CompressionNone {
		if _, err := jr.r.Seek(jr.blocks[n].Offset, io.SeekStart); err != nil {
			return bi, err
		}
		hdr := make([]byte, BlockHeaderLen+sfl)
		if _, err := io.ReadFull(jr.r, hdr); err != nil {
			return bi, err
		}
		bi.CompressedLen = int(binary.LittleEndian.Uint32(hdr[8+sfl : 12+sfl]))
	}

	// Load the block to read trailer (frame count, device table)
	// and get the actual base time from the block data.
	if err := jr.loadBlock(n); err != nil {
		return bi, err
	}
	bi.BaseTime = time.UnixMicro(jr.baseTimeUs)
	bi.FrameCount = jr.frameCount

	// Count device table entries.
	if jr.devTableOff+2 <= jr.blockSize {
		bi.DeviceCount = int(binary.LittleEndian.Uint16(jr.blockBuf[jr.devTableOff:]))
	}

	return bi, nil
}

// HasBlockIndex reports whether the file had a valid block index footer.
// Only meaningful for compressed files.
func (jr *Reader) HasBlockIndex() bool {
	if jr.compression == CompressionNone {
		return false
	}
	// Re-check for the index magic at EOF. This is cheap (2 seeks + 8 byte read).
	end, err := jr.r.Seek(0, io.SeekEnd)
	if err != nil || end < int64(FileHeaderSize)+8 {
		return false
	}
	if _, err := jr.r.Seek(end-8, io.SeekStart); err != nil {
		return false
	}
	var tail [8]byte
	if _, err := io.ReadFull(jr.r, tail[:]); err != nil {
		return false
	}
	return tail[4] == BlockIndexMagic[0] && tail[5] == BlockIndexMagic[1] &&
		tail[6] == BlockIndexMagic[2] && tail[7] == BlockIndexMagic[3]
}

// CurrentBlock returns the current block index, or -1 if before the first block.
func (jr *Reader) CurrentBlock() int {
	return jr.currentBlock
}

// Next advances to the next frame. Returns false when done or on error.
// If a frame within a block is corrupt, the remaining frames in that block
// are skipped and iteration continues with the next block.
func (jr *Reader) Next() bool {
	for {
		if jr.blockData != nil && jr.frameIdx < jr.frameCount {
			if jr.parseNextFrame() {
				return true
			}
			if jr.err != nil {
				// Frame-level error: skip the rest of this block and try the next one.
				jr.err = nil
				jr.blockData = nil
				continue
			}
		}

		nextBlock := jr.currentBlock + 1
		if nextBlock >= len(jr.blocks) {
			return false
		}
		if err := jr.loadBlock(nextBlock); err != nil {
			jr.err = err
			return false
		}
	}
}

// Frame returns the current frame. Only valid after Next returns true.
func (jr *Reader) Frame() Entry {
	return jr.entry
}

// Err returns the first error encountered during iteration.
func (jr *Reader) Err() error {
	return jr.err
}

// SeekBlock positions the reader at the start of block n (0-indexed).
func (jr *Reader) SeekBlock(n int) error {
	if n < 0 || n >= len(jr.blocks) {
		return fmt.Errorf("block %d out of range [0, %d)", n, len(jr.blocks))
	}
	return jr.loadBlock(n)
}

// SeekToTime finds the block containing the given timestamp via binary search
// and positions the reader at the start of that block.
func (jr *Reader) SeekToTime(t time.Time) error {
	if len(jr.blocks) == 0 {
		return fmt.Errorf("empty journal")
	}

	targetUs := t.UnixMicro()

	if jr.compression != CompressionNone {
		// Compressed: base times are already in memory.
		idx := sort.Search(len(jr.blocks), func(i int) bool {
			return jr.blocks[i].BaseTime > targetUs
		})
		if idx > 0 {
			idx--
		}
		return jr.loadBlock(idx)
	}

	// Uncompressed: read base times from disk (O(log n) seeks).
	var timeBuf [8]byte
	lo, hi := 0, len(jr.blocks)-1
	result := 0
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if _, err := jr.r.Seek(jr.blocks[mid].Offset, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.ReadFull(jr.r, timeBuf[:]); err != nil {
			return err
		}
		baseTimeUs := int64(binary.LittleEndian.Uint64(timeBuf[:]))
		if baseTimeUs <= targetUs {
			result = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}

	return jr.loadBlock(result)
}

// BlockDevices returns all device table entries for the current block.
func (jr *Reader) BlockDevices() []Device {
	if jr.blockData == nil {
		return nil
	}
	entries, _ := readDeviceTable(jr.blockData, jr.devTableOff)
	devices := make([]Device, len(entries))
	for i, e := range entries {
		devices[i] = e.toDevice()
	}
	return devices
}

// deviceTableEntry is a raw device table entry with ActiveFrom.
type deviceTableEntry struct {
	Source          uint8
	NAME            uint64
	ActiveFrom      uint32
	ProductCode     uint16
	ModelID         string
	SoftwareVersion string
	ModelVersion    string
	ModelSerial     string
}

func (e *deviceTableEntry) toDevice() Device {
	return Device{
		Source:          e.Source,
		NAME:            e.NAME,
		ProductCode:     e.ProductCode,
		ModelID:         e.ModelID,
		SoftwareVersion: e.SoftwareVersion,
		ModelVersion:    e.ModelVersion,
		ModelSerial:     e.ModelSerial,
	}
}

// BlockDevicesAt returns the resolved device table at the given frame index.
// For each source, the entry with the largest ActiveFrom <= frameIdx wins.
func (jr *Reader) BlockDevicesAt(frameIdx int) []Device {
	if jr.blockData == nil {
		return nil
	}
	entries, _ := readDeviceTable(jr.blockData, jr.devTableOff)

	best := make(map[uint8]deviceTableEntry)
	for _, e := range entries {
		if int(e.ActiveFrom) <= frameIdx {
			if cur, ok := best[e.Source]; !ok || e.ActiveFrom > cur.ActiveFrom {
				best[e.Source] = e
			}
		}
	}

	result := make([]Device, 0, len(best))
	for _, e := range best {
		result = append(result, e.toDevice())
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source < result[j].Source })
	return result
}

// loadBlock reads and validates block n, resetting the frame cursor.
func (jr *Reader) loadBlock(n int) error {
	if jr.compression != CompressionNone {
		return jr.loadCompressedBlock(n)
	}
	return jr.loadUncompressedBlock(n)
}

// loadUncompressedBlock reads a fixed-size block at the computed offset.
func (jr *Reader) loadUncompressedBlock(n int) error {
	off := jr.blocks[n].Offset
	if _, err := jr.r.Seek(off, io.SeekStart); err != nil {
		return fmt.Errorf("seek to block %d: %w", n, err)
	}
	if _, err := io.ReadFull(jr.r, jr.blockBuf); err != nil {
		return fmt.Errorf("read block %d: %w", n, err)
	}

	return jr.parseLoadedBlock(n)
}

// loadCompressedBlock reads and decompresses a variable-size block.
func (jr *Reader) loadCompressedBlock(n int) error {
	bi := jr.blocks[n]
	if _, err := jr.r.Seek(bi.Offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek to block %d: %w", n, err)
	}

	if jr.compression == CompressionZstdDict {
		return jr.loadDictCompressedBlock(n)
	}

	// Read header: BaseTime(8) [+ BaseSeq(8) for v2] + CompressedLen(4)
	sfl := jr.seqFieldLen()
	hdrLen := BlockHeaderLen + sfl
	hdr := make([]byte, hdrLen)
	if _, err := io.ReadFull(jr.r, hdr); err != nil {
		return fmt.Errorf("read block %d header: %w", n, err)
	}
	compressedLen := binary.LittleEndian.Uint32(hdr[8+sfl : 12+sfl])
	maxPayload, err := jr.blockPayloadLimit(n, hdrLen)
	if err != nil {
		return err
	}
	if compressedLen == 0 || int64(compressedLen) > maxPayload {
		return fmt.Errorf("block %d invalid compressed length %d (max %d)", n, compressedLen, maxPayload)
	}

	// Read compressed payload
	compressed := make([]byte, compressedLen)
	if _, err := io.ReadFull(jr.r, compressed); err != nil {
		return fmt.Errorf("read block %d compressed data: %w", n, err)
	}

	// Lazy-init zstd decoder
	if jr.zDecoder == nil {
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return fmt.Errorf("init zstd decoder: %w", err)
		}
		jr.zDecoder = dec
	}

	decompressed, err := jr.zDecoder.DecodeAll(compressed, jr.blockBuf[:0])
	if err != nil {
		return fmt.Errorf("decompress block %d: %w", n, err)
	}
	if len(decompressed) != jr.blockSize {
		return fmt.Errorf("block %d decompressed to %d bytes, expected %d", n, len(decompressed), jr.blockSize)
	}
	jr.blockBuf = decompressed[:jr.blockSize]

	return jr.parseLoadedBlock(n)
}

// loadDictCompressedBlock reads a block with per-block zstd dictionary.
// If DictLen=0, falls back to plain zstd decompression.
// Caller has already seeked to the block offset.
func (jr *Reader) loadDictCompressedBlock(n int) error {
	// Read header: BaseTime(8) [+ BaseSeq(8) for v2] + DictLen(4) + CompressedLen(4)
	sfl := jr.seqFieldLen()
	hdrLen := BlockHeaderLenDict + sfl
	hdr := make([]byte, hdrLen)
	if _, err := io.ReadFull(jr.r, hdr); err != nil {
		return fmt.Errorf("read block %d header: %w", n, err)
	}
	dictLen := binary.LittleEndian.Uint32(hdr[8+sfl : 12+sfl])
	compressedLen := binary.LittleEndian.Uint32(hdr[12+sfl : 16+sfl])
	maxPayload, err := jr.blockPayloadLimit(n, hdrLen)
	if err != nil {
		return err
	}
	if dictLen > uint32(maxPayload) {
		return fmt.Errorf("block %d invalid dictionary length %d (max %d)", n, dictLen, maxPayload)
	}
	if compressedLen == 0 || int64(compressedLen) > maxPayload-int64(dictLen) {
		return fmt.Errorf("block %d invalid compressed length %d with dict %d (max %d)", n, compressedLen, dictLen, maxPayload-int64(dictLen))
	}

	// Read dictionary (may be empty if training failed for this block)
	var dictData []byte
	if dictLen > 0 {
		dictData = make([]byte, dictLen)
		if _, err := io.ReadFull(jr.r, dictData); err != nil {
			return fmt.Errorf("read block %d dict: %w", n, err)
		}
	}

	// Read compressed payload
	compressed := make([]byte, compressedLen)
	if _, err := io.ReadFull(jr.r, compressed); err != nil {
		return fmt.Errorf("read block %d compressed data: %w", n, err)
	}

	var decompressed []byte
	if dictLen > 0 {
		// Per-block decoder with this block's dictionary
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderDicts(dictData))
		if err != nil {
			return fmt.Errorf("init zstd dict decoder for block %d: %w", n, err)
		}
		decompressed, err = dec.DecodeAll(compressed, jr.blockBuf[:0])
		dec.Close()
		if err != nil {
			return fmt.Errorf("decompress block %d: %w", n, err)
		}
	} else {
		// No dictionary, plain zstd.
		if jr.zDecoder == nil {
			dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
			if err != nil {
				return fmt.Errorf("init zstd decoder: %w", err)
			}
			jr.zDecoder = dec
		}
		var err error
		decompressed, err = jr.zDecoder.DecodeAll(compressed, jr.blockBuf[:0])
		if err != nil {
			return fmt.Errorf("decompress block %d: %w", n, err)
		}
	}

	if len(decompressed) != jr.blockSize {
		return fmt.Errorf("block %d decompressed to %d bytes, expected %d", n, len(decompressed), jr.blockSize)
	}
	jr.blockBuf = decompressed[:jr.blockSize]

	return jr.parseLoadedBlock(n)
}

// blockPayloadLimit returns the maximum payload bytes available after headerLen
// for block n, bounded by the next block offset or file end.
func (jr *Reader) blockPayloadLimit(n int, headerLen int) (int64, error) {
	start := jr.blocks[n].Offset
	var end int64
	if n+1 < len(jr.blocks) {
		end = jr.blocks[n+1].Offset
	} else {
		cur, err := jr.r.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, fmt.Errorf("seek current: %w", err)
		}
		end, err = jr.r.Seek(0, io.SeekEnd)
		if err != nil {
			return 0, fmt.Errorf("seek to end: %w", err)
		}
		if _, err := jr.r.Seek(cur, io.SeekStart); err != nil {
			return 0, fmt.Errorf("restore seek: %w", err)
		}
	}
	available := end - start - int64(headerLen)
	if available < 0 {
		return 0, fmt.Errorf("block %d has invalid boundaries", n)
	}
	return available, nil
}

// parseLoadedBlock validates the CRC and sets up the frame cursor from blockBuf.
func (jr *Reader) parseLoadedBlock(n int) error {
	bs := jr.blockSize

	stored := binary.LittleEndian.Uint32(jr.blockBuf[bs-4:])
	computed := crc32.Checksum(jr.blockBuf[:bs-4], CRC32cTable)
	if stored != computed {
		return fmt.Errorf("block %d checksum mismatch: stored=%08x computed=%08x", n, stored, computed)
	}

	trailerOff := bs - BlockTrailerLen
	devTableSize := int(binary.LittleEndian.Uint16(jr.blockBuf[trailerOff:]))
	devTableOff := bs - BlockTrailerLen - devTableSize
	frameCount := int(binary.LittleEndian.Uint32(jr.blockBuf[trailerOff+2:]))
	baseTimeUs := int64(binary.LittleEndian.Uint64(jr.blockBuf[0:8]))

	dataOffset := BlockDataOffsetV1
	var baseSeq uint64
	if jr.version == Version2 {
		dataOffset = BlockDataOffsetV2
		baseSeq = binary.LittleEndian.Uint64(jr.blockBuf[8:16])
	}

	jr.currentBlock = n
	jr.blockData = jr.blockBuf
	jr.blockOff = dataOffset
	jr.frameIdx = 0
	jr.frameCount = frameCount
	jr.baseTimeUs = baseTimeUs
	jr.lastTimeUs = baseTimeUs
	jr.devTableOff = devTableOff
	jr.baseSeqBlock = baseSeq

	return nil
}

// parseNextFrame decodes the frame at the current offset.
func (jr *Reader) parseNextFrame() bool {
	data := jr.blockData
	off := jr.blockOff
	limit := jr.devTableOff

	if off >= limit {
		jr.err = fmt.Errorf("frame data overrun at frame %d in block %d", jr.frameIdx, jr.currentBlock)
		return false
	}

	deltaUs, n := binary.Uvarint(data[off:limit])
	if n <= 0 {
		jr.err = fmt.Errorf("bad delta varint at frame %d in block %d", jr.frameIdx, jr.currentBlock)
		return false
	}
	off += n

	if off+4 > limit {
		jr.err = fmt.Errorf("truncated CANID at frame %d in block %d", jr.frameIdx, jr.currentBlock)
		return false
	}
	storedID := binary.LittleEndian.Uint32(data[off:])
	off += 4

	standard := storedID&0x80000000 != 0
	canID := storedID & 0x7FFFFFFF
	header := canbus.ParseCANID(canID)

	var frameData []byte
	if standard {
		if off+8 > limit {
			jr.err = fmt.Errorf("truncated standard frame at frame %d in block %d", jr.frameIdx, jr.currentBlock)
			return false
		}
		frameData = make([]byte, 8)
		copy(frameData, data[off:off+8])
		off += 8
	} else {
		dataLen, n := binary.Uvarint(data[off:limit])
		if n <= 0 {
			jr.err = fmt.Errorf("bad data length varint at frame %d in block %d", jr.frameIdx, jr.currentBlock)
			return false
		}
		off += n
		if off+int(dataLen) > limit {
			jr.err = fmt.Errorf("truncated extended frame at frame %d in block %d", jr.frameIdx, jr.currentBlock)
			return false
		}
		frameData = make([]byte, dataLen)
		copy(frameData, data[off:off+int(dataLen)])
		off += int(dataLen)
	}

	var tsUs int64
	if jr.frameIdx == 0 {
		tsUs = jr.baseTimeUs
	} else {
		tsUs = jr.lastTimeUs + int64(deltaUs)
	}
	jr.lastTimeUs = tsUs

	jr.entry = Entry{
		Timestamp: time.UnixMicro(tsUs),
		Header:    header,
		Data:      frameData,
	}
	jr.blockOff = off
	jr.frameIdx++
	return true
}

// readDeviceTable parses variable-length device table entries starting at the given offset.
//
// Entry format:
//
//	Source(1) + NAME(8) + ActiveFrom(4) + ProductCode(2) +
//	ModelIDLen(1) + ModelID + SWVersionLen(1) + SWVersion +
//	ModelVerLen(1) + ModelVersion + SerialLen(1) + Serial
func readDeviceTable(block []byte, offset int) ([]deviceTableEntry, error) {
	if offset+2 > len(block) {
		return nil, fmt.Errorf("device table offset out of range")
	}
	count := int(binary.LittleEndian.Uint16(block[offset:]))
	off := offset + 2

	entries := make([]deviceTableEntry, count)
	for i := range count {
		// Fixed part: Source(1) + NAME(8) + ActiveFrom(4) + ProductCode(2) = 15
		if off+15 > len(block) {
			return nil, fmt.Errorf("device table entry %d: fixed fields out of range", i)
		}
		entries[i].Source = block[off]
		entries[i].NAME = binary.LittleEndian.Uint64(block[off+1:])
		entries[i].ActiveFrom = binary.LittleEndian.Uint32(block[off+9:])
		entries[i].ProductCode = binary.LittleEndian.Uint16(block[off+13:])
		off += 15

		// Four length-prefixed strings.
		for _, dest := range []*string{
			&entries[i].ModelID,
			&entries[i].SoftwareVersion,
			&entries[i].ModelVersion,
			&entries[i].ModelSerial,
		} {
			s, n, err := ReadLenPrefixedString(block, off)
			if err != nil {
				return nil, fmt.Errorf("device table entry %d: %w", i, err)
			}
			*dest = s
			off += n
		}
	}
	return entries, nil
}
