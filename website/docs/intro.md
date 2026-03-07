---
slug: /
sidebar_position: 1
title: Introduction
---

# lplex

**CAN bus HTTP bridge for NMEA 2000.** lplex reads raw CAN frames from a SocketCAN interface, reassembles fast-packets, tracks device discovery, and streams frames to clients over SSE. It supports journaling, cloud replication, and PGN decoding.

## What lplex does

lplex sits between your boat's NMEA 2000 bus and your applications. It handles all the low-level CAN bus complexity (fast-packet reassembly, address claiming, device discovery) and exposes a clean HTTP/SSE interface that any language can consume.

```
  NMEA 2000 Bus
       |
   SocketCAN (can0)
       |
     lplex                          lplex-cloud
       |                                |
   HTTP/SSE (:8089)    ---- gRPC ----> HTTP/SSE (:8080)
       |                (mTLS)          |
  Local clients                    Remote clients
  (lplexdump, apps)               (dashboards, APIs)
```

## Key features

- **SSE streaming** with two modes: ephemeral (fire-and-forget) and buffered (cursor-based with replay and acknowledgment)
- **Device discovery** via PGN 60928 address claims and PGN 126996 product info, with automatic ISO Request probing
- **Journaling** to `.lpj` files with zstd compression, block indexing, and configurable rotation
- **Cloud replication** over gRPC with mTLS for remote access to boat data
- **PGN decoding** via a custom DSL that generates Go structs, decoders, and protobuf definitions
- **Filtering** by PGN, manufacturer, device instance, or CAN NAME
- **Client libraries** for [Go](/integration/go-client) and [TypeScript](/integration/typescript-client)
- **Embeddable**: use the broker as a Go library in your own applications

## Components

| Binary | Description | Platforms |
|---|---|---|
| `lplex` | Boat server (CAN reader, broker, HTTP, journaling, replication) | Linux (SocketCAN) |
| `lplex-cloud` | Cloud server (gRPC receiver, per-instance brokers, HTTP API) | Linux |
| `lplexdump` | CLI client (SSE consumer, PGN decoder, journal replay) | Linux, macOS |

## Architecture

lplex uses a single-goroutine broker design with no locks in the hot path:

```
CANReader goroutine
  |  reads CAN frames, reassembles fast-packets
  v
rxFrames channel
  |
Broker goroutine (single writer, owns all state)
  |  assigns monotonic sequence numbers
  |  appends pre-serialized JSON to ring buffer (64k entries)
  |  updates device registry
  |  fans out to subscribers and consumers
  |  feeds journal channel (if enabled)
  |
  +---> Ring buffer (pre-serialized JSON, RLock for reads)
  +---> DeviceRegistry (source -> device info)
  +---> ValueStore (last frame per source+PGN)
  +---> Consumers (pull-based, cursor + filter + notify)
  +---> Subscribers (push-based, ephemeral channels)
  +---> JournalWriter (optional, zstd-compressed blocks)
  +---> ReplicationClient (optional, gRPC to cloud)
```

## Next steps

- [Installation](/getting-started/installation) to get lplex running
- [Quick Start](/getting-started/quick-start) for a hands-on walkthrough
- [HTTP API](/integration/http-api) reference for building integrations
- [PGN DSL](/pgn-dsl/overview) to understand how PGN definitions work
