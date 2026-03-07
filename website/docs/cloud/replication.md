---
sidebar_position: 3
title: Replication
---

# Replication Protocol

lplex replicates data from boat to cloud over gRPC with mTLS authentication. The protocol uses three RPCs: Handshake, Live, and Backfill.

## Connection flow

```
Boat (lplex)                              Cloud (lplex-cloud)
     |                                           |
     |--- Handshake (instance_id, head_seq) ---->|  Verify cert CN matches instance_id
     |<-- HandshakeResponse (cursor, holes) -----|  Load/create InstanceState
     |                                           |
     |--- Live stream (frames) ----------------->|  Feed into replica Broker
     |<-- LiveStatus (ack_seq) ---- periodic ----|  Cursor advancement
     |                                           |
     |--- Backfill stream (blocks) ------------->|  Write via BlockWriter
     |<-- BackfillStatus (filled ranges) --------|  Update HoleTracker
     |                                           |
```

## Handshake

Unary RPC. Establishes the session.

**Boat sends:**
- Instance ID (must match mTLS certificate CN)
- Current head sequence number
- Total journal bytes available

**Cloud responds:**
- Cloud cursor (last contiguous sequence)
- List of holes (sequence gaps that need backfilling)
- Whether the instance is new or reconnecting

On reconnect, the cloud compares the boat's head with its own cursor and creates holes for any gaps.

## Live stream

Bidirectional streaming RPC for real-time frame delivery.

**Boat sends:** `LiveFrame` messages containing:
- Sequence number
- Timestamp
- CAN header (priority, PGN, source, destination)
- Payload data

**Cloud sends:** Periodic `LiveStatus` messages with `ack_seq` (the highest contiguous sequence received). The boat uses this for flow control and status reporting.

Live frames flow immediately after handshake. There's no batching delay. The cloud feeds each frame into the instance's replica Broker, which handles ring buffer insertion, device registry updates, and fan-out to HTTP/SSE clients.

## Backfill stream

Bidirectional streaming RPC for filling historical gaps with raw journal blocks.

**Boat sends:** Raw journal blocks byte-for-byte, along with metadata (base sequence, block length, file offset).

**Cloud sends:** `BackfillStatus` messages indicating which sequence ranges have been filled.

### Zero-copy passthrough

Backfill sends journal blocks without decompressing or re-encoding them. The cloud writes them directly via BlockWriter. This is efficient for both CPU and bandwidth (blocks are already zstd-compressed).

### Hole tracking

The `HoleTracker` maintains a sorted list of sequence intervals (holes) that need data:

```
Holes: [(1000, 2000), (5000, 5500)]
```

When a backfill block arrives covering sequences 1000-1500:
```
Holes: [(1500, 2000), (5000, 5500)]
```

When all holes are filled, backfill completes and the stream closes gracefully.

## Boat-side configuration

Add to your `lplex.conf`:

```hocon
replication {
  target = "lplex.example.com:9443"
  instance-id = "boat-001"
  tls {
    cert = "/etc/lplex/boat-001.crt"
    key = "/etc/lplex/boat-001.key"
    ca = "/etc/lplex/ca.crt"
  }
}
```

Or via CLI flags:

```bash
lplex -interface can0 \
  -replication-target lplex.example.com:9443 \
  -replication-instance-id boat-001 \
  -replication-tls-cert /etc/lplex/boat-001.crt \
  -replication-tls-key /etc/lplex/boat-001.key \
  -replication-tls-ca /etc/lplex/ca.crt
```

## Monitoring replication

### Boat side

```bash
curl http://localhost:8089/replication/status
```

```json
{
  "connected": true,
  "instance_id": "boat-001",
  "local_head_seq": 50000,
  "cloud_cursor": 49950,
  "holes": [],
  "live_lag": 50,
  "backfill_remaining_seqs": 0,
  "last_ack": "2026-03-06T10:15:30Z"
}
```

### Cloud side

```bash
curl https://lplex.example.com/instances/boat-001/status
```

```bash
# Diagnostic event log
curl https://lplex.example.com/instances/boat-001/replication/events?limit=50
```

## Reconnection

The replication client automatically reconnects with exponential backoff. On reconnect:

1. Handshake exchanges current positions
2. Cloud creates holes for any gaps
3. Live stream resumes from the current head
4. Backfill starts filling the new holes

No data is lost as long as the boat has journal files covering the gap period.

## State persistence

The cloud persists replication state per instance in `{data-dir}/{instance-id}/state.json`. This includes the cursor, hole list, and last-seen timestamp. On restart, it resumes from the persisted state.
