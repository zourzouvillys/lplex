---
sidebar_position: 2
title: Architecture
---

# Architecture Deep Dive

This page walks through the internal design of lplex for contributors who want to understand the codebase.

## Single-goroutine broker

The Broker is the heart of lplex. It runs in a single goroutine and owns all mutable frame routing state. This eliminates locks on the hot path.

```
                     ┌─────────────────────────────────────────────────┐
                     │              Broker goroutine                    │
  rxFrames ────────> │  1. Assign monotonic sequence number             │
  (channel)          │  2. Pre-serialize frame as JSON                  │
                     │  3. Append to ring buffer                        │
                     │  4. Update device registry (if PGN 60928/126996)│
                     │  5. Update value store (last frame per src+PGN)  │
                     │  6. Fan out to ephemeral subscribers (channels)  │
                     │  7. Notify pull-based consumers (non-blocking)   │
                     │  8. Send to journal channel (non-blocking)       │
                     │  9. Send ISO Request for unknown sources          │
                     └─────────────────────────────────────────────────┘
```

**Why single-goroutine?** It makes the broker simple to reason about. All state mutations happen in one place. Consumers and HTTP handlers read shared state through RLock (ring buffer) or RWMutex (device registry, value store).

## Ring buffer

The ring buffer is a fixed-size array (default 64k entries, power of 2) of pre-serialized JSON frames. The broker writes at the head; consumers read from their own positions.

```
  head (write position)
    │
    v
  ┌───┬───┬───┬───┬───┬───┬───┬───┐
  │ N │...│...│...│...│...│...│N-1│
  └───┴───┴───┴───┴───┴───┴───┴───┘
    ^                           ^
    │                           │
  newest                    oldest
```

Key properties:
- Power-of-2 size for bit-mask indexing (no modulo)
- Frames are serialized to JSON once at write time, not per-consumer
- Consumers hold an RLock while reading (no contention with the writer since the writer doesn't hold a lock on the ring)
- No allocations on the hot path (reuses ring entries)

## Pre-serialized JSON

When a frame enters the broker, it's immediately serialized to a `[]byte` JSON representation and stored in the ring. When an SSE client reads it, the pre-serialized bytes are written directly to the response. This means serialization cost is O(1) regardless of the number of consumers.

## Consumer model

Buffered clients use a pull-based Consumer that reads from a tiered log:

```
Consumer.Next(ctx)
    │
    ├── 1. Check journal files (oldest data)
    │      - Discover files in journal dir
    │      - Binary search for seq in v2 files
    │      - Read forward through blocks
    │
    ├── 2. Check ring buffer (recent data)
    │      - If cursor is within ring range, read directly
    │
    └── 3. Block on live notification channel
           - Wait for broker to signal new frames
           - Read from ring buffer
```

Consumers iterate at their own pace. A slow consumer reading from journal files doesn't affect a fast consumer reading from the ring buffer.

If a consumer's cursor is behind both the journal and ring buffer (data no longer available), it gets `ErrFallenBehind`.

## Fast-packet reassembly

NMEA 2000 PGNs larger than 8 bytes are fragmented into "fast-packets" (multiple CAN frames). The `FastPacketAssembler` reassembles these before they enter the broker.

Each fast-packet has:
- Frame 0: sequence counter (3 bits) + total length (1 byte) + first 6 data bytes
- Frames 1-N: sequence counter (3 bits) + frame counter (5 bits) + 7 data bytes

The assembler tracks in-progress assemblies keyed by (source, PGN, sequence counter) and emits the complete payload when all frames arrive.

`IsFastPacket(pgnNum)` identifies fast-packet PGNs at runtime. It checks `pgn.Registry` first (where the `FastPacket` flag is code-generated from the DSL's `fast_packet` attribute), then falls back to a legacy map for PGNs not yet in the DSL.

## Device registry

The `DeviceRegistry` maps source addresses to device information. It's populated from:
- **PGN 60928** (ISO Address Claim): provides CAN NAME, manufacturer code, device class/function/instance
- **PGN 126996** (Product Information): provides model ID, software version, serial number

When the broker sees a frame from an unknown source, it sends an ISO Request (PGN 59904) asking that source for its address claim. This automatic probing discovers all devices on the bus.

The registry uses an RWMutex. The broker writes (under Write lock), HTTP handlers read (under Read lock).

## Value store

The `ValueStore` tracks the last-seen frame for each (source address, PGN) pair. Used by the `/values` and `/values/decoded` endpoints to show the most recent data from each device.

## Journal writer

The `JournalWriter` runs in its own goroutine, reading from a 16384-entry channel. It accumulates frames into a memory buffer and flushes complete blocks to disk.

```
Broker ──(non-blocking send)──> journal chan ──> JournalWriter goroutine
                                                  │
                                                  ├── Buffer frames in memory
                                                  ├── When block is full:
                                                  │    1. Build device table
                                                  │    2. Compute CRC32C
                                                  │    3. Compress with zstd (optional)
                                                  │    4. Write to file
                                                  │    5. Update block index
                                                  └── On rotation trigger:
                                                       1. Finalize current block
                                                       2. Write block index
                                                       3. Close file
                                                       4. Open new file
                                                       5. Notify OnRotate callback
```

## Replication

### Live stream
The `ReplicationClient` creates a Consumer positioned at the head and streams each frame to the cloud via gRPC. The cloud feeds frames into a replica Broker.

### Backfill
A separate goroutine reads raw journal blocks from disk and sends them byte-for-byte to the cloud. The cloud writes them directly via `BlockWriter`. No decompression or re-encoding on either side.

### Hole tracking
The `HoleTracker` maintains a sorted list of sequence intervals. On reconnect, the handshake compares positions and creates holes for gaps. Backfill fills holes until none remain.

## Package dependency graph

```
cmd/lplex ──┐
cmd/lplex-cloud ──┤
cmd/lplexdump ────┤
                  ├──> lplex (root)
                  │      ├──> canbus
                  │      ├──> journal
                  │      ├──> pgn
                  │      └──> proto/replication/v1
                  │
cmd/pgngen ───────┴──> pgngen

lplexc ──────────────> pgn
```
