---
sidebar_position: 3
title: Journal Format
---

# Journal Binary Format (.lpj)

The `.lpj` format is a block-based binary recording of reassembled NMEA 2000 CAN frames. Each file is self-contained: a consumer can read it like a live stream and build up device state from the frames.

## File header (16 bytes)

| Offset | Size | Field |
|---|---|---|
| 0 | 3 | Magic: `"LPJ"` (0x4C 0x50 0x4A) |
| 3 | 1 | Version: `0x01` or `0x02` |
| 4 | 4 | BlockSize: uint32 LE (bytes, power of 2, default 262144, min 4096) |
| 8 | 4 | Flags: uint32 LE, bits 0-7 = CompressionType |
| 12 | 4 | Reserved: uint32 LE (0) |

### Versions

- **v1** (`0x01`): Time-based seeking only. No sequence numbers in blocks.
- **v2** (`0x02`): Adds `BaseSeq` (uint64 LE) to block headers for sequence-based seeking.

### Compression types

| Value | Mode | Block layout |
|---|---|---|
| 0 | None | Fixed-size blocks, O(1) byte-offset seeking |
| 1 | zstd | Variable-size blocks with block index |
| 2 | zstd+dict | Per-block dictionary, variable-size blocks with block index |

## Uncompressed block layout

Each block is exactly `BlockSize` bytes.

```
┌──────────────────────────────────────────────────────────────┐
│ +0       BaseTime (8 bytes, int64 LE, unix microseconds)      │
│ +8       BaseSeq  (8 bytes, uint64 LE) [v2 only]             │
├──────────────────────────────────────────────────────────────┤
│ +8/+16   Frame data                                           │
│          [delta] [CANID] [8B data]          (standard)        │
│          [delta] [CANID] [len] [data]       (extended)        │
│          ...                                                  │
├──────────────────────────────────────────────────────────────┤
│          Zero padding                                         │
├──────────────────────────────────────────────────────────────┤
│          Device table (variable-length entries)                │
│          EntryCount: uint16 LE                                │
│          Entry[0..N-1]                                        │
├──────────────────────────────────────────────────────────────┤
│ +(BlockSize-10)  Trailer (10 bytes)                           │
│          DeviceTableSize: uint16 LE                           │
│          FrameCount: uint32 LE                                │
│          Checksum: uint32 LE (CRC32C of [0..BlockSize-4))     │
└──────────────────────────────────────────────────────────────┘
```

## Compressed block layout

Each compressed block has a header followed by the compressed payload.

### zstd (type 1)

```
┌──────────────────────────────────────────────────────────────┐
│ Header (12 bytes v1 / 20 bytes v2)                            │
│   BaseTime:      int64 LE  (unix microseconds)                │
│   BaseSeq:       uint64 LE [v2 only]                          │
│   CompressedLen: uint32 LE                                    │
├──────────────────────────────────────────────────────────────┤
│ CompressedData (CompressedLen bytes)                           │
│   zstd frame, decompresses to BlockSize bytes                 │
└──────────────────────────────────────────────────────────────┘
```

### zstd+dict (type 2)

```
┌──────────────────────────────────────────────────────────────┐
│ Header (16 bytes v1 / 24 bytes v2)                            │
│   BaseTime:      int64 LE                                     │
│   BaseSeq:       uint64 LE [v2 only]                          │
│   DictLen:       uint32 LE                                    │
│   CompressedLen: uint32 LE                                    │
├──────────────────────────────────────────────────────────────┤
│ DictData (DictLen bytes)                                      │
│   zstd dictionary trained from this block's data              │
├──────────────────────────────────────────────────────────────┤
│ CompressedData (CompressedLen bytes)                           │
│   zstd frame compressed with DictData                         │
└──────────────────────────────────────────────────────────────┘
```

The decompressed block has the same layout as an uncompressed block. CRC32C is computed on the uncompressed data and verified after decompression.

## Frame encoding

Two variants, selected by bit 31 of the CANID uint32:

### Standard-length (bit 31 = 1)

Data is exactly 8 bytes. No DataLen field.

| Field | Encoding | Size |
|---|---|---|
| DeltaUs | unsigned varint | 1-3 bytes |
| CANID | uint32 LE (bit 31 set) | 4 bytes |
| Data | fixed 8 bytes | 8 bytes |

Total: 13-15 bytes. Covers ~90-95% of frames (all standard single-frame PGNs).

### Extended-length (bit 31 = 0)

Variable-length data with explicit length.

| Field | Encoding | Size |
|---|---|---|
| DeltaUs | unsigned varint | 1-3 bytes |
| CANID | uint32 LE (bit 31 clear) | 4 bytes |
| DataLen | unsigned varint | 1-2 bytes |
| Data | DataLen bytes | variable |

Used for reassembled fast-packets (up to 1785 bytes) and rare short frames.

### Reader logic

```
canid = read uint32 LE
if canid & 0x80000000:
    canid &= 0x7FFFFFFF  // mask off bit 31
    data = read 8 bytes
else:
    dataLen = read varint
    data = read dataLen bytes
```

## Device table

Located at `BlockSize - 10 - DeviceTableSize` within the block.

| Field | Size | Description |
|---|---|---|
| EntryCount | uint16 LE | Number of entries |

Per entry (variable length, minimum 19 bytes):

| Field | Size | Description |
|---|---|---|
| Source | uint8 | Source address (0-253) |
| NAME | uint64 LE | 64-bit ISO NAME |
| ActiveFrom | uint32 LE | Frame index where this binding became active (0 = before block start) |
| ProductCode | uint16 LE | PGN 126996 product code (0 = unknown) |
| ModelID | length-prefixed string | Model identifier |
| SoftwareVersion | length-prefixed string | Software version |
| ModelVersion | length-prefixed string | Model/hardware version |
| ModelSerial | length-prefixed string | Serial number |

Length-prefixed strings: 1-byte length followed by that many bytes. Empty strings have length 0.

### ActiveFrom semantics

- `ActiveFrom = 0`: device was known before this block (carried over from previous state)
- `ActiveFrom > 0`: device was discovered (or changed source address) at that frame index within the block
- Multiple entries for the same source address: the one with the largest `ActiveFrom <= targetFrame` is active at that frame
- This handles mid-block address claim conflicts correctly

## Block index

Appended at file close for compressed files only.

```
┌──────────────────────────────────────┐
│ Offset[0]:  uint64 LE                │
│ Offset[1]:  uint64 LE                │
│ ...                                  │
│ Offset[N-1]: uint64 LE              │
├──────────────────────────────────────┤
│ Count: uint32 LE                     │
│ Magic: "LPJI" (4 bytes)             │
└──────────────────────────────────────┘
```

Reading: seek to EOF-8, read Count + Magic. If Magic == `"LPJI"`, seek to EOF - 8 - Count*8 and read the offset table. On crash/truncation (no valid magic), fall back to forward-scanning block headers.

## Seeking

### Time-based (both v1 and v2)

**Uncompressed**: binary search reading BaseTime at `16 + mid * BlockSize`. O(log N) disk reads.

**Compressed**: read block index from EOF, binary search in-memory. O(log N) comparisons + 1 read + 1 decompress.

### Sequence-based (v2 only)

Same algorithms, searching on BaseSeq instead of BaseTime. Frame at index `i` in a block has `seq = BaseSeq + i`.

## Size estimates

At ~200 frames/sec typical bus rate:

| Metric | Uncompressed | zstd (~4x) |
|---|---|---|
| Throughput | ~2.7 KB/s | ~0.7 KB/s |
| Per hour | ~10 MB | ~2.5 MB |
| Per day | ~233 MB | ~58 MB |
| Per month | ~7 GB | ~1.7 GB |

Device table overhead: ~1000-1600 bytes per block (20 devices with product info).
Block index overhead: ~600 bytes/hour (negligible).
