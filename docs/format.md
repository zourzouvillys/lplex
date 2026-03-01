 N2K Journal: Block-Based Binary CAN Frame Recording

 Context

 lplex receives NMEA 2000 CAN frames, reassembles fast-packets, discovers devices, and streams to SSE clients. We want to record this data to disk as a journal for future replay. Each journal file should be self-contained: a consumer reads it like a
 live stream and naturally builds up device state from the frames themselves.

 Key Design Decisions

 Block-based format. Fixed-size blocks (default 256KB). Each block has an absolute timestamp header, packed frame data, a device table, and a CRC32C-checked trailer. No record type tags.

 Standard-length flag in CANID. The 29-bit CAN ID occupies bits 0-28 of a uint32, leaving bits 29-31 spare. Bit 31 = 1 means "data is exactly 8 bytes, no DataLen field." This eliminates the 1-byte DataLen varint on ~90-95% of frames (all standard
 single-frame PGNs). Only reassembled fast-packets and rare short frames need the extended format.

 Per-block device table with sequence tracking. Each block's trailer contains device registry entries with an ActiveFrom field: the frame index (within the block) where that NAME→Source binding became active. This correctly handles mid-block source
 address changes (address claim conflicts) and lets consumers reconstruct the device state at any point within a block.

 Record reassembled frames, not raw CAN fragments. Zero changes to CANReader. Tap at broker level.

 Separate goroutine with batched block writes. Frames buffered in memory, written as complete blocks. 16384-entry channel between broker and writer. On crash, up to one block lost; all completed blocks intact with checksums.

 Block-level compression. Optional zstd compression at the block level. Each block is built in memory identically to the uncompressed format, then compressed as a unit before writing. CRC is over the uncompressed data, validated after decompression. A block index at end of file enables O(1) seeking; if missing (crash), the reader forward-scans compressed block headers.

 Binary Format

 File Header (16 bytes)

 Offset  Size  Field
 0       3     Magic: "LPJ" (0x4C 0x50 0x4A)
 3       1     Version: 0x01 or 0x02
 4       4     BlockSize: uint32 LE (bytes, power of 2, default 262144, min 4096)
 8       4     Flags: uint32 LE, bits 0-7 = CompressionType (0=none, 1=zstd, 2=zstd+dict)
 12      4     Reserved: uint32 LE (0)

 Version 0x02 adds BaseSeq (uint64 LE) to block headers for sequence-based seeking.
 The reader supports both v1 and v2. v1 files have no seq info; consumers treat them
 as unavailable for seq-based seeking.

 CompressionType values:
   0 = none (uncompressed, fixed-size blocks)
   1 = zstd (compressed, variable-size blocks with block index)
   2 = zstd+dict (per-block dictionary compressed, variable-size blocks with block index)

 Uncompressed Block Layout (CompressionType=0, BlockSize bytes)

 v1: frame data starts at +8.
 v2: BaseSeq at +8, frame data starts at +16.

 ┌──────────────────────────────────────────────────────────┐
 │ +0       BaseTime (8 bytes, int64 LE)                    │  Unix microseconds, first frame
 │ +8       BaseSeq  (8 bytes, uint64 LE) [v2 only]        │  Seq of first frame in block
 ├──────────────────────────────────────────────────────────┤
 │ +8/+16   Frame data                                      │
 │          [delta] [CANID] [8B data]          (standard)   │  bit 31 of CANID = 1
 │          [delta] [CANID] [len] [data]       (extended)   │  bit 31 of CANID = 0
 │          ...                                             │
 ├──────────────────────────────────────────────────────────┤
 │          Zero padding                                    │
 ├──────────────────────────────────────────────────────────┤
 │ +(BlockSize-10-DeviceTableSize)                           │
 │          Device table (variable-length entries)           │
 │          EntryCount: uint16 LE                           │
 │          [Src:u8, NAME:u64, ActiveFrom:u32,              │
 │           ProductCode:u16, 4x len-prefixed strings] * N  │
 ├──────────────────────────────────────────────────────────┤
 │ +BlockSize-10   Fixed trailer (10 bytes)                 │
 │          DeviceTableSize: uint16 LE                      │
 │          FrameCount: uint32 LE                           │
 │          Checksum: uint32 LE (CRC32C of [0..BlockSize-4))│
 └──────────────────────────────────────────────────────────┘

 Compressed Block Layout (CompressionType>0)

 Each compressed block is preceded by a header, followed by the compressed payload.

 v1: 12-byte header (BaseTime + CompressedLen).
 v2: 20-byte header (BaseTime + BaseSeq + CompressedLen).

 ┌──────────────────────────────────────────────────────────┐
 │ Block Header (12 bytes v1 / 20 bytes v2)                │
 │   BaseTime:       int64 LE (8 bytes, unix microseconds)  │  Duplicated from block, enables seeking
 │   BaseSeq:        uint64 LE (8 bytes) [v2 only]         │  without decompression
 │   CompressedLen:  uint32 LE (4 bytes)                    │
 ├──────────────────────────────────────────────────────────┤
 │ CompressedData (CompressedLen bytes)                     │
 │   zstd-compressed full block (decompresses to BlockSize) │
 └──────────────────────────────────────────────────────────┘

 The decompressed block has the exact same layout as an uncompressed block (BaseTime, frame data, device table, CRC32C trailer). CRC is computed on uncompressed data and validated after decompression.

 Dictionary Compressed Block Layout (CompressionType=2)

 Each block carries its own zstd dictionary, making it independently decompressible with zero external state.

 v1: 16-byte header (BaseTime + DictLen + CompressedLen).
 v2: 24-byte header (BaseTime + BaseSeq + DictLen + CompressedLen).

 ┌──────────────────────────────────────────────────────────┐
 │ Block Header (16 bytes v1 / 24 bytes v2)                │
 │   BaseTime:       int64 LE (8 bytes, unix microseconds)  │
 │   BaseSeq:        uint64 LE (8 bytes) [v2 only]         │
 │   DictLen:        uint32 LE (4 bytes, dictionary size)   │
 │   CompressedLen:  uint32 LE (4 bytes, payload size)      │
 ├──────────────────────────────────────────────────────────┤
 │ DictData (DictLen bytes)                                 │
 │   zstd dictionary trained from this block's data         │
 ├──────────────────────────────────────────────────────────┤
 │ CompressedData (CompressedLen bytes)                     │
 │   zstd-compressed full block using DictData              │
 │   (decompresses to BlockSize)                            │
 └──────────────────────────────────────────────────────────┘

 Total on-disk block size: 16 + DictLen + CompressedLen.

 Dictionary Training: for each block the writer splits the uncompressed data into overlapping 256-byte samples, extracts an 8KB history from the frame data region, and calls zstd.BuildDict. The resulting dictionary (~8KB) provides pre-built entropy tables and match references tuned to the block's content. Even though the dictionary is trained from the same data it compresses, the entropy tables and backreferences provide a meaningful compression improvement on the highly repetitive CAN bus data.

 Forward scan for type 2: read 16-byte header, extract DictLen and CompressedLen, skip DictLen + CompressedLen bytes to find the next block.

 Block Index (appended at file close, compressed files only)

 ┌──────────────────────────────────────────────────────────┐
 │ Offset[0]:  uint64 LE (file offset of block 0)          │
 │ Offset[1]:  uint64 LE                                   │
 │ ...                                                     │
 │ Offset[N-1]: uint64 LE                                  │
 ├──────────────────────────────────────────────────────────┤
 │ Count:  uint32 LE (number of blocks)                    │
 │ Magic:  "LPJI" (4 bytes)                                │
 └──────────────────────────────────────────────────────────┘

 Total overhead: Count * 8 + 8 bytes. For 150 blocks/hour: 1208 bytes.

 To read: seek to EOF-8, read Count(4) + Magic(4). If Magic == "LPJI", seek to EOF - 8 - Count*8 and read the offset table. If no valid magic (crash, truncation), fall back to forward-scanning through block headers.

 Frame Encoding

 Two variants, selected by bit 31 of the stored CANID uint32:

 Standard-length (bit 31 = 1): data is exactly 8 bytes, no DataLen field.
 DeltaUs    varint     Microseconds since previous frame (0 for first in block)
 CANID      uint32 LE  29-bit CAN ID | 0x80000000 (bit 31 set)
 Data       8 bytes    Fixed 8-byte payload
 Size: 1-3 (delta) + 4 (CANID) + 8 (data) = 13-15 bytes

 Extended-length (bit 31 = 0): variable-length data with explicit length.
 DeltaUs    varint     Microseconds since previous frame
 CANID      uint32 LE  29-bit CAN ID (bit 31 clear)
 DataLen    varint     Payload length (0-1785)
 Data       DataLen    Payload bytes
 Size: 1-3 (delta) + 4 (CANID) + 1-2 (len) + N (data)

 Reader logic: read CANID uint32, check canid & 0x80000000. If set, mask it off (canid &= 0x7FFFFFFF) and read 8 bytes. If clear, read DataLen varint then that many bytes.

 Writer logic: if len(data) == 8, set bit 31 and skip DataLen. Otherwise, clear bit 31 and write DataLen varint.

 Device Table

 Located at (BlockSize - 10 - DeviceTableSize) within the block. The trailer stores DeviceTableSize (bytes of device table including the 2-byte entry count), and the reader computes the offset. Contains a log of NAME→Source bindings with product info, each with the frame index where the binding became active.

 EntryCount:  uint16 LE
 For each entry (variable length, min 19 bytes):
     Source:       uint8      Source address (0-253)
     NAME:         uint64 LE  64-bit ISO NAME from PGN 60928
     ActiveFrom:   uint32 LE  Frame index within block (0 = active from block start)
     ProductCode:  uint16 LE  PGN 126996 product code (0 = unknown)
     ModelIDLen:   uint8      Length of ModelID string
     ModelID:      N bytes    PGN 126996 model identifier
     SWVersionLen: uint8      Length of SoftwareVersion string
     SWVersion:    N bytes    PGN 126996 software version
     ModelVerLen:  uint8      Length of ModelVersion string
     ModelVersion: N bytes    PGN 126996 model version
     SerialLen:    uint8      Length of ModelSerial string
     ModelSerial:  N bytes    PGN 126996 serial number

 Semantics:
 - Entries with ActiveFrom = 0: device was known before this block started (carried over)
 - Entries with ActiveFrom > 0: device was discovered (or source changed) at that frame
 - Multiple entries for the same source: the one with the largest ActiveFrom <= targetFrame is active at targetFrame
 - To find who's at source S at frame N: scan entries for Source == S, pick the one with max ActiveFrom <= N
 - Product info fields come from PGN 126996 and may be empty (zero-length strings, ProductCode=0) if not yet discovered

 Example: Device A at source 5 from frame 0, device B takes over source 5 at frame 1500:
 {Source: 5, NAME: A_name, ActiveFrom: 0, ProductCode: 1234, ModelID: "GNX 120", ...}
 {Source: 5, NAME: B_name, ActiveFrom: 1500, ProductCode: 0, ModelID: "", ...}

 Writer: when flushing a block, the writer tracks which PGN 60928 frames it saw during the block. It builds the device table from:
 1. A snapshot of the registry at block start (all entries with ActiveFrom=0), including product info
 2. Any address claims that appeared as frames within the block (ActiveFrom = frame index), with product info looked up from the device registry

 Varints

 Standard unsigned LEB128. encoding/binary.PutUvarint / binary.ReadUvarint.

 Seeking

 Uncompressed (CompressionType=0):
 1. Block count = (fileSize - 16) / BlockSize
 2. Binary search: read int64 LE at offset 16 + mid * BlockSize
 3. Find block where BaseTime <= target < nextBlock.BaseTime
 4. Read device table via DeviceTableSize in trailer for instant device context
 5. Parse frames within block to find exact position
 O(log N) reads. 1-hour file: ~190 blocks, ~8 search steps.

 Compressed (CompressionType>0):
 1. Read block index from EOF (or forward-scan if missing)
 2. Binary search in-memory BaseTime array (zero I/O)
 3. Seek to block offset, read + decompress block
 4. Parse frames within decompressed block
 O(log N) in-memory comparison + 1 read + 1 decompress.

 Sequence-based seeking (v2 only):
 Same algorithms as time-based, but searches on BaseSeq instead of BaseTime.
 Frame at index i in a v2 block has seq = BaseSeq + i.
 Used by Consumer for tiered replay: journal files -> ring buffer -> live.

 Size Estimates

 Uncompressed at 200 fps, ~95% standard-length frames:
 - Standard: 13 bytes * 190 frames/sec = 2470 B/s
 - Extended: 18 bytes avg * 10 frames/sec = 180 B/s
 - Total: ~2.7 KB/s = ~9.5 MB/hour

 With zstd compression (~4x ratio at 256KB blocks on CAN data):
 - ~2.4 MB/hour
 - Block index overhead: ~600 bytes/hour (negligible)

 Device table: ~1000-1600 bytes per block (20 devices with product info, ~50-80 bytes/entry). Block overhead still negligible relative to frame data.

 Rotation

 Configurable triggers (checked per block flush):
 - Duration: wall-clock time since file creation (default: 1 hour)
 - Size: total bytes written (default: 0, disabled)
 - Count: total frame records (default: 0, disabled)

 Rotation: finalize block → write block index (compressed only) → sync → close file → open new file → write header.

 File naming: {dir}/{prefix}-{YYYYMMDD}T{HHMMSS.sss}Z.lpj
