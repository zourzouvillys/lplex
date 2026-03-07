---
sidebar_position: 4
title: Journaling
---

# Journaling

lplex can record all frames to disk in `.lpj` journal files. These are binary files optimized for sequential writes and efficient seeking by time or sequence number.

## Enabling journaling

Set the journal directory in your config or via CLI flag:

```hocon
journal {
  dir = /var/log/lplex
  prefix = nmea2k
  block-size = 262144
  compression = zstd

  rotate {
    duration = PT1H
    size = 0
  }
}
```

Or with flags:

```bash
lplex -interface can0 -journal-dir /var/log/lplex
```

With journaling enabled, lplex creates files like:

```
/var/log/lplex/nmea2k-20260306T101500Z.lpj
/var/log/lplex/nmea2k-20260306T111500Z.lpj
```

## Block format

Journal files are organized into blocks (default 256 KB). Each block contains:

- **BaseTime**: first frame timestamp in the block (8 bytes)
- **BaseSeq**: first frame sequence number (8 bytes, v2 only)
- **Frame data**: length-prefixed frames with delta-encoded timestamps
- **Device table**: snapshot of known devices at block time
- **CRC32C checksum**: integrity verification

Blocks are self-contained. You can read any block independently without reading earlier blocks.

## Compression

Compression is applied per-block. Three modes are available:

| Mode | Flag value | Description |
|---|---|---|
| None | `none` | Fixed-size blocks, O(1) seeking |
| zstd | `zstd` | Variable-size blocks with block index, ~4x compression |
| zstd+dict | `zstd-dict` | Per-block dictionary training, slightly better ratio |

`zstd` is the default and recommended for most use cases. It reduces journal size by roughly 4x with minimal CPU overhead.

With compression enabled, a block index is appended at the end of each file for O(1) offset lookup. If a file is crash-truncated (no index), the reader falls back to a forward scan.

### Storage estimates

At typical NMEA 2000 bus rates (~2.7 KB/s uncompressed):

| Duration | Uncompressed | zstd (~4x) |
|---|---|---|
| 1 hour | ~10 MB | ~2.5 MB |
| 24 hours | ~233 MB | ~58 MB |
| 30 days | ~7 GB | ~1.7 GB |

## File rotation

Files rotate based on duration, size, or both:

```hocon
journal {
  rotate {
    duration = PT1H    # New file every hour
    size = 104857600   # Or every 100 MB, whichever comes first
  }
}
```

Set a value to `0` to disable that trigger. At least one should be non-zero.

## Replaying journals

Use `lplexdump` to replay journal files:

```bash
# Normal speed
lplexdump -file recording.lpj

# 10x speed
lplexdump -file recording.lpj -speed 10

# As fast as possible
lplexdump -file recording.lpj -speed 0

# With decoding
lplexdump -file recording.lpj -decode

# Seek to a time
lplexdump -file recording.lpj -start 2026-03-06T10:30:00Z
```

## Inspecting journals

Use `-inspect` to see the structure of a journal file without replaying it:

```bash
lplexdump -file recording.lpj -inspect
```

This shows block boundaries, timestamps, sequence ranges, device tables, compression ratios, and integrity status.

## Consumer fallback

When a buffered client falls behind the ring buffer, the Consumer automatically falls back to reading from journal files. It discovers journal files in the configured directory, seeks to the appropriate sequence number, and reads forward until it catches up with the ring buffer.

If the requested data is not available in either the journal or ring buffer, the consumer returns `ErrFallenBehind`.
