# Cloud Replication

lplex supports replicating CAN bus data from a boat to a cloud instance over gRPC. The boat initiates all connections (no public IP required), streams live frames for real-time access, and backfills historical gaps from journal files. Connectivity can be intermittent (dock wifi, starlink). When the boat reconnects, live data flows immediately while gaps are filled in the background.

## Overview

```
BOAT                                         CLOUD (lplex-cloud)

lplex server                                 gRPC server (:9443, mTLS)
  Broker ─── Consumer                          ReplicationServer
       |         |                               |
       |    ReplicationClient ──── gRPC ────── Handshake (unary)
       |         |                               |
       |         ├── Live stream ─────────────── Live handler ── InstanceState
       |         |   (realtime frames)           |                   |
       |         └── Backfill stream ─────────── Backfill handler   Broker (replica)
       |             (raw journal blocks)        |                   |
       |                                         |              JournalWriter
  JournalWriter                              HTTP server (:8080)
       |                                         |
  .lpj files                                     +── GET /instances
                                                 +── GET /instances/{id}/status
                                                 +── GET /instances/{id}/events (SSE)
                                                 +── GET /instances/{id}/devices
```

Two binaries:
- **`lplex`** (boat): runs `ReplicationClient` alongside the existing broker when replication is configured
- **`lplex-cloud`** (cloud): runs `ReplicationServer` (gRPC) + HTTP API for web clients

## Protocol

Three gRPC RPCs on a single service, defined in `proto/replication/v1/replication.proto`:

### Handshake (unary)

Exchanged on every connect/reconnect. Identifies the boat, reports its head sequence, and receives the cloud's sync state.

```
Boat                                    Cloud
  |                                       |
  |── HandshakeRequest ────────────────>  |
  |   instance_id: "boat-001"            |  verify mTLS cert CN == instance_id
  |   head_seq: 50000                    |  load/create InstanceState
  |   journal_bytes: 52428800            |  if reconnect: add hole [cursor+1, head_seq)
  |                                       |
  |  <──────────────── HandshakeResponse  |
  |   cursor: 48000                      |  cursor = continuous data through this seq
  |   holes: [{49001, 50000}]            |  holes = gaps cloud is missing
  |   live_start_from: 50000             |  where to start live stream
```

**First connect** (new instance): cursor=0, no holes, live_start_from=1.

**Reconnect**: If cursor=48000 and boat's head is now 50000, the cloud creates a hole for the gap. The boat starts live from the new head and backfills holes concurrently.

### Live (bidirectional stream)

Realtime frame delivery. The boat reads from a `Consumer` at its broker's current head and sends each frame as a `LiveFrame`. The cloud feeds frames into a replica `Broker` which fans out to SSE clients.

```
Boat                                    Cloud
  |                                       |
  |── LiveFrame(seq=50001, ...) ───────>  |  feed into instance Broker
  |── LiveFrame(seq=50002, ...) ───────>  |  advance cursor if continuous
  |── LiveFrame(seq=50003, ...) ───────>  |
  |── LiveStatus(head=50003) ──────────>  |  update boat_head_seq
  |                                       |
  |  <──────── LiveAck(acked_through=50003)  periodic ACK + persist
```

Messages:
- `LiveFrame`: seq, timestamp_us, can_id (29-bit CAN ID), data (up to 8 bytes)
- `LiveStatus`: periodic boat head_seq + journal_bytes (sent every ~5s)
- `LiveAck`: cloud confirms receipt through a given seq

### Backfill (bidirectional stream)

Bulk transfer of raw journal blocks to fill holes. Blocks are sent as-is from `.lpj` files (no decompression on the boat, no re-encoding on the cloud). Holes are processed newest-first so recent data arrives before ancient history.

```
Boat                                    Cloud
  |                                       |
  |── Block(base_seq=49001, ...) ──────>  |  write to journal via BlockWriter
  |                                       |  fill hole in HoleTracker
  |  <── BackfillAck ─────────────────── |
  |   continuous_through: 49050          |  updated cursor
  |   remaining: [{49051, 50000}]        |  holes still unfilled
  |                                       |
  |── Block(base_seq=49051, ...) ──────>  |
  |  <── BackfillAck ─────────────────── |
  |   continuous_through: 49100          |
  |   remaining: [{49101, 50000}]        |
```

Block message fields:
- `base_seq`: first sequence number in the block
- `base_time_us`: first timestamp
- `frame_count`: number of frames
- `data`: raw block bytes (compressed or uncompressed, passthrough from journal)
- `compressed`: whether data is zstd-compressed
- `block_size`: uncompressed block size (needed to reconstruct valid journal files)

### Why Three RPCs

1. **Independent flow control**: gRPC applies backpressure per-stream. Slow backfill (disk I/O bound) doesn't stall live frames.
2. **Clean lifecycle**: Live can reconnect independently of backfill. Backfill can finish and close while live keeps running.
3. **Simpler code**: Each handler has one job. No multiplexing logic in application code.
4. **Unary handshake**: Simple request/response for auth + state exchange.

## Authentication

mTLS with per-boat client certificates:
- Cloud CA signs a client cert per boat
- Cert CN (Common Name) contains the instance ID (e.g. `boat-001`)
- gRPC server requires and verifies client certs
- Handshake handler verifies cert CN matches the requested `instance_id`
- Live and Backfill streams identify the instance via mTLS cert CN, falling back to `x-instance-id` gRPC metadata (used in tests without TLS)

## Cloud Side: InstanceManager

The cloud maintains per-instance state in `{data-dir}/instances/{id}/`:

```
{data-dir}/
  instances/
    boat-001/
      journal/           # .lpj files (backfill blocks + flushed live frames)
      state.json         # persisted InstanceState (cursor, holes, boat head)
    boat-002/
      journal/
      state.json
```

Each instance gets a lazy `Broker` started on first connection (~3MB RAM + 2 goroutines). The broker runs in replica mode: it honors sequence numbers from the boat instead of auto-incrementing, and skips CAN bus ISO requests. Connected SSE clients read from the replica broker just like they would from a local broker.

### Hole Tracking

`HoleTracker` maintains a sorted list of `[start, end)` sequence ranges representing gaps. Operations:
- **Add**: insert a hole, merge overlapping/adjacent ranges
- **Fill**: remove a range from holes (split, trim, or delete)
- **ContinuousThrough**: highest seq with no holes below it

Typical case: 0-3 holes. All operations are linear on the hole count.

## Boat Side: ReplicationClient

`ReplicationClient` connects to the cloud, performs a handshake, then runs two concurrent goroutines:

1. **Live goroutine**: Creates a `Consumer` from the broker's current head, sends `LiveFrame` for each new frame, periodic `LiveStatus` every 5s. Reads `LiveAck` responses.
2. **Backfill goroutine**: Reads raw blocks from journal files for each hole (newest-first), sends `Block` messages. Reads `BackfillAck` to track remaining holes.

On disconnect, exponential backoff (1s, 2s, 4s, ... capped at 60s). Re-handshake tells the client what the cloud now has. A new hole forms for the gap.

## Configuration

### Boat (`lplex`)

CLI flags:
| Flag | Description |
|---|---|
| `-replication-target` | Cloud gRPC address (host:port) |
| `-replication-instance-id` | Instance ID (must match cert CN) |
| `-replication-tls-cert` | Client certificate for mTLS |
| `-replication-tls-key` | Client private key |
| `-replication-tls-ca` | CA certificate for server verification |

HOCON:
```hocon
replication {
  target = "cloud.example.com:9443"
  instance-id = "boat-001"
  tls {
    cert = "/etc/lplex/boat.crt"
    key = "/etc/lplex/boat.key"
    ca = "/etc/lplex/ca.crt"
  }
}
```

### Cloud (`lplex-cloud`)

CLI flags:
| Flag | Default | Description |
|---|---|---|
| `-grpc-listen` | `:9443` | gRPC listen address |
| `-http-listen` | `:8080` | HTTP listen address |
| `-data-dir` | `/data/lplex` | Data directory for instance state + journals |
| `-tls-cert` | | Server certificate |
| `-tls-key` | | Server private key |
| `-tls-client-ca` | | CA certificate for client verification |

HOCON (`lplex-cloud.conf`):
```hocon
grpc {
  listen = ":9443"
  tls {
    cert = "/etc/lplex-cloud/server.crt"
    key = "/etc/lplex-cloud/server.key"
    client-ca = "/etc/lplex-cloud/ca.crt"
  }
}
http {
  listen = ":8080"
}
data-dir = "/data/lplex"
```

## Cloud HTTP API

| Endpoint | Description |
|---|---|
| `GET /instances` | List all instances with summary state |
| `GET /instances/{id}/status` | Detailed instance status (cursor, holes, lag) |
| `GET /instances/{id}/events` | SSE stream from instance's replica broker |
| `GET /instances/{id}/devices` | Device table from instance's broker |

### Status Response Example

```json
{
  "id": "boat-001",
  "connected": true,
  "cursor": 148500,
  "boat_head_seq": 150000,
  "boat_journal_bytes": 52428800,
  "holes": [{"start": 100000, "end": 120000}],
  "lag_seqs": 1500,
  "last_seen": "2026-03-02T10:15:30Z"
}
```

### Boat Status Endpoint

When replication is enabled, `lplex` exposes `GET /replication/status`:

```json
{
  "connected": true,
  "instance_id": "boat-001",
  "local_head_seq": 150000,
  "cloud_cursor": 148500,
  "holes": [{"start": 100000, "end": 120000}],
  "live_lag": 1500,
  "backfill_remaining_seqs": 20000,
  "last_ack": "2026-03-02T10:15:00Z"
}
```

## Broker Replica Mode

When `BrokerConfig.ReplicaMode` is true:
- `handleFrame` uses the provided `frame.Seq` instead of auto-incrementing
- Head tracks the highest received seq + 1
- ISO request broadcasts are skipped (no CAN bus on cloud)
- `BrokerConfig.InitialHead` sets starting position (for resuming from persisted cursor)

## Data Flow

### Live Frames

1. Boat broker produces frame with seq
2. ReplicationClient's Consumer receives it via `Next(ctx)`
3. Sends `LiveFrame` on gRPC stream
4. Cloud Live handler feeds `RxFrame{Seq: frame.Seq, ...}` into replica broker
5. Replica broker writes to ring buffer, fans out to SSE clients, feeds JournalWriter
6. Cursor advances if frame extends the continuous range

### Backfill Blocks

1. ReplicationClient reads raw blocks from `.lpj` files via `journal.Reader`
2. Sends `Block` with raw bytes (no decompression)
3. Cloud Backfill handler writes block via `BlockWriter.AppendBlock()`
4. `BlockWriter` validates CRC32C, writes to journal file with proper headers
5. `HoleTracker.Fill()` removes the filled range
6. ACK returns updated cursor + remaining holes
