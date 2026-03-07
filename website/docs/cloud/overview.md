---
sidebar_position: 1
title: Overview
---

# Cloud Overview

`lplex-cloud` is the cloud counterpart to `lplex`. It receives boat data over gRPC, stores it, and re-serves it via HTTP/SSE. Multiple boats can replicate to a single cloud instance.

## Architecture

```
Boat 1 (lplex)                       lplex-cloud
  |                                      |
  +-- Live gRPC stream ---- mTLS ------>-+-- InstanceManager
  +-- Backfill gRPC stream ------------>-|     +-- Instance "boat-001"
                                         |     |     +-- Broker (replica mode)
Boat 2 (lplex)                          |     |     +-- BlockWriter
  |                                      |     |     +-- HoleTracker
  +-- Live gRPC stream ---- mTLS ------>-+     +-- Instance "boat-002"
  +-- Backfill gRPC stream ------------>-|           +-- Broker (replica mode)
                                         |           +-- BlockWriter
                                         |           +-- HoleTracker
                                         |
                                         +-- HTTP Server (:8080)
                                         |     +-- GET /instances
                                         |     +-- GET /instances/{id}/events
                                         |     +-- GET /instances/{id}/devices
                                         |     +-- GET /instances/{id}/values
                                         |     +-- GET /instances/{id}/status
                                         |
                                         +-- JournalKeeper (shared)
```

## Key design decisions

- **Live-first**: on connect, live frames flow immediately. Backfill runs concurrently to fill historical gaps.
- **Raw block passthrough**: backfill sends journal blocks byte-for-byte. Zero decompression or re-encoding on either side.
- **Lazy broker per instance**: each boat gets its own replica Broker, started on demand (~3 MB RAM + 2 goroutines). Stopped after idle timeout.
- **Separate streams**: live and backfill have independent flow control and lifecycle.
- **Hole tracking**: a sorted interval list tracks sequence gaps. Handshake creates holes on reconnect; backfill fills them.

## Cloud HTTP API

All endpoints are prefixed with `/instances/{id}` where `{id}` is the instance ID (matches the boat's `-replication-instance-id`).

### Instance management

#### `GET /instances`

List all known instances.

```json
{
  "instances": [
    {
      "id": "boat-001",
      "connected": true,
      "cursor": 50000,
      "boat_head_seq": 50050,
      "holes": [],
      "lag_seqs": 50,
      "last_seen": "2026-03-06T10:15:30Z"
    }
  ]
}
```

#### `GET /instances/{id}/status`

Detailed replication status for a specific instance.

```json
{
  "id": "boat-001",
  "connected": true,
  "cursor": 50000,
  "boat_head_seq": 50050,
  "boat_journal_bytes": 1073741824,
  "holes": [
    {"start": 10000, "end": 10500}
  ],
  "lag_seqs": 50,
  "last_seen": "2026-03-06T10:15:30Z"
}
```

### Per-instance data

These endpoints mirror the boat-side API:

| Endpoint | Description |
|---|---|
| `GET /instances/{id}/events` | SSE stream from the replica broker |
| `GET /instances/{id}/devices` | Device snapshot |
| `GET /instances/{id}/values` | Last-seen values |
| `GET /instances/{id}/values/decoded` | Last-seen values with PGN decoding |

All support the same filter query parameters as the boat-side endpoints.

### Replication diagnostics

#### `GET /instances/{id}/replication/events`

Recent replication diagnostic events for an instance.

**Query parameter:** `?limit=100` (default 100, max 1024)

```json
[
  {"time": "2026-03-06T10:15:00Z", "type": "live_start", "detail": "connected from 10.0.1.5"},
  {"time": "2026-03-06T10:15:01Z", "type": "backfill_start", "detail": "filling 500 seqs"},
  {"time": "2026-03-06T10:15:15Z", "type": "block_received", "detail": "block 42, 256KB"},
  {"time": "2026-03-06T10:15:15Z", "type": "backfill_stop", "detail": "all holes filled"},
  {"time": "2026-03-06T10:20:00Z", "type": "checkpoint", "detail": "cursor=50000"}
]
```

Event types: `live_start`, `live_stop`, `backfill_start`, `backfill_stop`, `block_received`, `checkpoint`.

### Health and metrics

#### `GET /healthz`

```json
{
  "status": "ok",
  "instances_total": 5,
  "instances_connected": 3
}
```

#### `GET /metrics`

Prometheus metrics:

| Metric | Description |
|---|---|
| `lplex_cloud_instances_total` | Total known instances |
| `lplex_cloud_instances_connected` | Currently connected instances |
| `lplex_cloud_instance_connected{instance}` | Per-instance connection state |
| `lplex_cloud_instance_lag_seqs{instance}` | Sequence lag per instance |
| `lplex_cloud_instance_cursor{instance}` | Cloud cursor per instance |
| `lplex_cloud_instance_holes{instance}` | Number of holes per instance |

## gRPC protocol

The replication protocol uses three RPCs defined in `proto/replication/v1/`:

1. **Handshake** (unary): instance identification, cursor exchange, hole detection
2. **Live** (bidirectional stream): real-time frame delivery
3. **Backfill** (bidirectional stream): raw journal block transfer

See [Replication](/cloud/replication) for the full protocol walkthrough.
