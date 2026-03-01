 Add Block-Level Compression to Journal                                                                                                                                                                                                                       
                                                                                                                                                                                                                                                              
 Context                                                                                                                                                                                                                                                      
                                                                                                                                                                                                                                                              
 The journal writes fixed-size 64KB blocks of CAN frame data to disk at ~9.5 MB/hour (~228 MB/day). CAN bus data is highly compressible (repeated PGNs, similar timestamps, lots of zero padding between frame data and device table). Block-level
 compression with zstd should cut storage 3-5x. The on-disk format should specify the compression algorithm so we can swap in different codecs later.

 Design

 On-Disk Format Changes

 File header (16 bytes, unchanged size): use the existing reserved Flags field (offset 8) to store the compression algorithm in bits 0-7. Version stays at 0x01.

 Offset  Size  Field
 0       3     Magic: "LPJ"
 3       1     Version: 0x01
 4       4     BlockSize: uint32 LE (uncompressed block size, same as before)
 8       4     Flags: uint32 LE, bits 0-7 = CompressionType (0=none, 1=zstd)
 12      4     Reserved

 Uncompressed blocks (CompressionType=0): identical to current format. Fixed-size, O(1) seeking.

 Compressed blocks (CompressionType>0): variable-size on disk. Each block is preceded by a 12-byte header:

 BaseTime        8 bytes   int64 LE (duplicated uncompressed, enables seeking without decompression)
 CompressedLen   4 bytes   uint32 LE
 CompressedData  N bytes   zstd-compressed full block (decompresses to BlockSize bytes)

 The decompressed block has the exact same layout as today (BaseTime at +0, frames, device table, trailer with CRC32C). CRC is computed on uncompressed data and validated after decompression.

 Block index (appended at file close):

 Offset[0]    8 bytes   uint64 LE, file offset of block 0
 Offset[1]    8 bytes   ...
 ...
 Count        4 bytes   uint32 LE, number of blocks
 Magic        4 bytes   "LPJI"

 Total overhead: Count * 8 + 8 bytes. For 150 blocks/hour: 1208 bytes. To read: seek to EOF-8, read Count(4) + Magic(4). If Magic == "LPJI", seek to EOF - 8 - Count*8 and read the offset table. If no valid magic (crash/truncation), fall back to
 forward-scanning through block headers.

 Reader Flow (compressed)

 On open:
 1. Read file header, extract CompressionType from Flags
 2. Try to read block index from end of file
 3. If no index: forward-scan (read 12-byte header, skip CompressedLen bytes, repeat) to build in-memory offset + baseTime table
 4. Store []blockInfo{Offset int64, BaseTime int64} for all blocks

 loadBlock(n):
 1. Seek to blocks[n].Offset
 2. Read 12-byte header (BaseTime + CompressedLen)
 3. Read CompressedLen bytes
 4. Decompress with zstd to blockBuf (BlockSize bytes)
 5. Validate CRC32C (same as today)
 6. Parse trailer, set up frame cursor (same as today)

 SeekToTime(t):
 - Binary search over in-memory blocks[].BaseTime array. No I/O during search.

 Writer Flow (compressed)

 flushBlock():
 1. Build uncompressed block in w.block (same as today: frames + device table + CRC trailer)
 2. Compress w.block with zstd EncodeAll
 3. Write 12-byte header: [BaseTime:8][CompressedLen:4]
 4. Write compressed payload
 5. Record block offset in w.blockOffsets

 finalize():
 1. Flush last block (if any)
 2. Write block index (offsets + count + magic)
 3. Sync + close

 Dependency

 Add github.com/klauspost/compress for zstd package. Most popular Go compression library, 12k+ stars, actively maintained. Reuse a single zstd.Encoder and zstd.Decoder instance per writer/reader.

 Changes

 1. go.mod

 - Add github.com/klauspost/compress

 2. journal/format.go

 - Add CompressionType uint8 enum: CompressionNone = 0, CompressionZstd = 1
 - Add BlockIndexMagic = [4]byte{'L','P','J','I'}
 - Add BlockHeaderLen = 12 (BaseTime + CompressedLen, for compressed blocks)

 3. journal/reader.go

 - Add blockInfo struct: Offset int64, BaseTime int64
 - Replace blockCount int with blocks []blockInfo
 - NewReader: read Flags from header, read/build block index, store compression type
 - Add readBlockIndex(r io.ReadSeeker, fileSize int64) ([]blockInfo, error)
 - Add scanBlocks(r io.ReadSeeker, fileSize int64) ([]blockInfo, error) (forward-scan fallback)
 - loadBlock(n): branch on compression type, decompress if needed
 - SeekToTime: binary search in-memory blocks[].BaseTime (no I/O during search)
 - BlockCount(): return len(jr.blocks)
 - Add zstd decoder field, lazy-init on first compressed block

 4. internal/server/journal.go

 - JournalConfig: add Compression journal.CompressionType
 - JournalWriter: add zstd encoder field, blockOffsets []int64, compressBuf []byte
 - NewJournalWriter: init zstd encoder if compression enabled
 - openFile: write CompressionType into Flags field of file header
 - flushBlock: if compressed, compress full block then write 12-byte header + payload; else write block as-is (current path)
 - finalize: write block index after last block flush (compressed mode only)
 - Track file byte offset for block index

 5. cmd/lplex/main.go

 - Add -journal-compression flag (string: "none", "zstd"; default "zstd")

 6. docs/format.md

 - Add compressed block layout section
 - Add block index format section
 - Document Flags field semantics
 - Update size estimates with compression ratios

 7. internal/server/journal_test.go

 - Add TestJournalCompressedRoundTrip: write + read frames with compression enabled
 - Add TestJournalCompressedBlockBoundary: 700 frames across compressed blocks
 - Add TestJournalCompressedTimeSeeking: binary search with compressed blocks
 - Add TestJournalCompressedCrashResilience: truncated compressed file, forward-scan recovery
 - Add TestJournalBlockIndex: verify index is written and read correctly
 - Add TestJournalBlockIndexMissing: verify forward-scan fallback works
 - Add TestJournalUncompressedStillWorks: ensure CompressionNone path is unchanged
 - Existing tests remain unchanged (they use CompressionNone by default)

 Verification

 go build ./...
 golangci-lint run
 go test ./internal/server/... ./journal/... -v -count=1

 Then deploy to inuc1.local with -journal-compression zstd, let it record for a minute, and:
 # check compressed file size vs old
 ls -la /datalog/lplex/journal/

 # replay compressed file
 lplexdump -file /datalog/lplex/journal/<new-file>.lpj

 Verify: device table populates, frames decode correctly, seeking works.
