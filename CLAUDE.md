# lplex

CAN bus HTTP bridge for NMEA 2000. Reads raw CAN frames from a SocketCAN interface, reassembles fast-packets, tracks device discovery, and streams frames to clients over SSE with session management, filtering, and replay. Supports cloud replication over gRPC for remote access to boat data.

## Build & Test

```bash
go build ./...                  # build all
go build -o lplex ./cmd/lplex   # build server
go build -o lplex-cloud ./cmd/lplex-cloud  # build cloud server
go build -o lplexdump ./cmd/lplexdump
go test ./... -v -count=1       # run tests
golangci-lint run               # lint (must pass before pushing)
make proto                      # regenerate protobuf (requires protoc + plugins)
```

## Release

Tags trigger GoReleaser via GitHub Actions, which builds binaries, .deb, Docker images, and pushes the Homebrew formula.

```bash
git tag -a v0.2.0 -m "v0.2.0" && git push origin v0.2.0
```

**Release artifacts**:
- `lplex` server: Linux amd64/arm64 only (no macOS, needs SocketCAN)
- `lplex-cloud` cloud server: Linux amd64/arm64
- `lplexdump` client: Linux + macOS (amd64/arm64)
- `.deb` package: bundles both binaries + systemd unit
- Docker: `ghcr.io/sixfathoms/lplex` (linux/amd64 + linux/arm64)
- Homebrew: `sixfathoms/tap/lplexdump` (client only, pushed to `sixfathoms/homebrew-tap`)

**Secrets**: `HOMEBREW_TAP_TOKEN` (fine-grained PAT with Contents read/write on `sixfathoms/homebrew-tap`)

## Deployment

- **Host**: `inuc1.local` (Linux x86_64)
- **Install**: `.deb` package from GitHub Releases
- **Service**: `lplex.service` (systemd)
- **Config**: `/etc/lplex/lplex.conf` (HOCON) or `/etc/default/lplex` (`LPLEX_ARGS="-interface can0 -port 8089"`)
- **CAN interface**: `can0`
- **HTTP port**: 8089

## Architecture

Single-goroutine broker design, no locks in the hot path:

```
SocketCAN (can0)
    |
CANReader goroutine
    |  reads extended CAN frames
    |  reassembles fast-packets (multi-frame PGNs)
    |
    v
rxFrames chan
    |
Broker goroutine (single writer, owns all state)
    |  assigns monotonic sequence numbers
    |  appends pre-serialized JSON to ring buffer (64k entries, power-of-2)
    |  updates device registry (PGN 60928 address claim, PGN 126996 product info)
    |  fans out to ephemeral subscribers (with per-client filtering)
    |  notifies pull-based consumers (non-blocking send to notify channels)
    |  sends ISO requests to discover new devices on the bus
    |  non-blocking send to journal channel (if enabled)
    |
    +---> ring buffer ([]ringEntry, lock-free writes, RLock for consumer reads)
    +---> DeviceRegistry (RWMutex, keyed by source address)
    +---> consumers map (pull-based: cursor, filter, notify chan)
    +---> sessions map (buffered client metadata: cursor, filter, timeout)
    +---> subscribers map (ephemeral clients: channels, filters, no state)
    +---> journal chan (16384-entry buffer, optional)
    |
    v
HTTP Server (:8089)                    JournalWriter goroutine
    |                                       |  reads from journal chan
    +-- GET  /events                        |  encodes frames into 256KB blocks
    +-- PUT  /clients/{id}                  |  writes blocks with CRC32C checksums
    +-- GET  /clients/{id}/events           |  rotates files by duration/size
    +-- PUT  /clients/{id}/ack              |  tracks device table per block
    +-- POST /send                          v
    +-- GET  /devices                  .lpj journal files (v2, with BaseSeq)
    +-- GET  /replication/status
                                       Consumer (pull-based reader)
CANWriter goroutine                        |  reads from tiered log:
    |  reads from txFrames chan            |  1. journal files (oldest)
    |  fragments fast-packets for TX      |  2. ring buffer (recent)
    |  writes to SocketCAN                |  3. live notification (blocking wait)

ReplicationClient (optional)
    |  gRPC connection to cloud
    +-- Live goroutine: Consumer at head -> LiveFrame stream
    +-- Backfill goroutine: raw journal blocks -> Block stream
    +-- Reconnect loop: exponential backoff, re-handshake
```

### Cloud Architecture

```
lplex-cloud process
  +-- gRPC server (:9443, mTLS)
  |     +-- Handshake: verify cert CN, load/create InstanceState
  |     +-- Live handler: feed frames into instance Broker (replica mode)
  |     +-- Backfill handler: write raw blocks via BlockWriter
  +-- HTTP server (:8080)
  |     +-- GET /instances
  |     +-- GET /instances/{id}/status
  |     +-- GET /instances/{id}/events (SSE)
  |     +-- GET /instances/{id}/devices
  +-- InstanceManager
        +-- Per-instance state (InstanceState with HoleTracker)
        +-- Lazy Broker lifecycle (~3MB RAM + 2 goroutines per active instance)
        +-- Persistent state (state.json per instance)
```

## Package Structure

| Package | Owns |
|---|---|
| `lplex` (root) | Public core: `Broker`, `Server`, `Consumer`, `CANReader`, `CANWriter`, `JournalWriter`, `DeviceRegistry`, `FastPacketAssembler`, `ReplicationClient`, `ReplicationServer`, `InstanceManager`, `HoleTracker`, `BlockWriter`, filters, ring buffer. Embeddable by external Go services. |
| `cmd/lplex/` | Boat server: flag parsing, HOCON config, signal handling, mDNS registration, wires broker + CAN I/O + HTTP + optional replication |
| `cmd/lplex-cloud/` | Cloud server: gRPC + HTTP servers, InstanceManager, mTLS, HOCON config |
| `cmd/lplexdump/` | CLI client: SSE consumer with pretty-print, device table, auto-reconnect |
| `lplexc/` | Public Go client library: Subscribe, Devices, Send, Session, mDNS discovery |
| `canbus/` | Public CAN ID parsing (`CANHeader`, `ParseCANID`, `BuildCANID`) and ISO NAME decoding |
| `journal/` | Public journal format: `Device`, `Reader`, `CompressionType`, block constants, length-prefixed string helpers |
| `proto/replication/v1/` | Protobuf + gRPC definitions for replication protocol |

### Root Package File Map

| File | Owns |
|---|---|
| `broker.go` | `Broker`, `BrokerConfig` (including `ReplicaMode`, `InitialHead`), `ClientSession`, `subscriber`, `EventFilter`, ring buffer, fan-out, session lifecycle, ephemeral subscriptions, consumer registry, journal feed |
| `consumer.go` | `Consumer`, `Frame`, `ErrFallenBehind`, pull-based tiered reader (journal -> ring -> live), journal fallback with file discovery and seq-based seeking |
| `server.go` | `Server`, HTTP handlers, ephemeral + buffered SSE streaming, filter query param parsing, ISO 8601 duration parser |
| `can.go` | `CANReader` (SocketCAN rx + fast-packet reassembly), `CANWriter` (SocketCAN tx + fragmentation) |
| `canid.go` | Thin wrappers re-exporting `canbus.ParseCANID`, `canbus.BuildCANID` |
| `fastpacket.go` | `FastPacketAssembler`, `FragmentFastPacket`, fast-packet PGN registry |
| `devices.go` | `DeviceRegistry`, PGN 60928/126996 decoding, manufacturer lookup table |
| `journal_writer.go` | `JournalWriter`, `JournalConfig`, block encoding, zstd compression, block index, file rotation, device table tracking (with product info) |
| `replication.go` | `SeqRange`, `HoleTracker`, `SyncState`, hole tracking algorithm |
| `replication_client.go` | `ReplicationClient`, `ReplicationClientConfig`, `ReplicationStatus`, live stream + backfill + reconnect loop |
| `replication_server.go` | `ReplicationServer`, `InstanceManager`, `InstanceState`, `InstanceStatus`, `InstanceSummary`, gRPC handlers (Handshake, Live, Backfill), mTLS verification, state persistence |
| `block_writer.go` | `BlockWriter`, `BlockWriterConfig`, raw block append to journal files with file rotation |
| `doc.go` | Package documentation with embedding example |

## Client Modes

Two connection modes, ephemeral is the default:

### Ephemeral (default)

`GET /events` with optional query param filters (`?pgn=129025&manufacturer=Garmin`). No session, no replay, no ACK.

```
lplexdump -server http://inuc1.local:8089
```

### Buffered

`PUT /clients/{id}` with `buffer_timeout`, then `GET /clients/{id}/events`, and periodic `PUT /clients/{id}/ack`.

```
lplexdump -server http://inuc1.local:8089 -buffer-timeout PT5M
```

## Cloud Replication

Boat-to-cloud data replication over gRPC with mTLS. Three RPCs: unary Handshake, bidirectional Live stream (realtime frames), bidirectional Backfill stream (raw journal blocks). See [docs/cloud-replication.md](docs/cloud-replication.md) for the full protocol specification.

Key design decisions:
- **Live-first**: On connect, live frames flow immediately. Backfill runs concurrently.
- **Raw block passthrough**: Backfill sends journal blocks byte-for-byte. Zero decompression/re-encoding.
- **Lazy Broker per instance**: Cloud starts a replica Broker on demand, stops after idle timeout.
- **Separate streams**: Live and backfill have independent flow control and lifecycle.
- **Hole tracking**: Sorted interval list tracks gaps. Handshake creates holes on reconnect, backfill fills them.

## Key Design Decisions

- **Pull-based Consumer model**: buffered clients use a Kafka/Kinesis-style Consumer that reads from a tiered log (journal -> ring buffer -> live notification). Each consumer iterates at its own pace via `Next(ctx)`. Sessions store only metadata (cursor, filter, timeout); the HTTP handler creates a Consumer on each connection.
- **Pre-serialized JSON in ring buffer**: frames are serialized once when received, not per-client.
- **Resolved filters**: device-based filters are flattened to source addresses at consumer creation time, avoiding device registry lookups during iteration.
- **Ephemeral subscribers are separate from sessions**: ephemeral `/events` uses push-based channels. Session-based `/clients/{id}/events` uses pull-based Consumer.
- **ISO Request on unknown source**: broker discovers devices automatically.
- **Journal v2 format**: blocks include `BaseSeq` for O(log n) sequence-based seeking. Reader supports both v1 (time-only) and v2 (time + seq). Consumer falls back to journal files when behind the ring buffer, returning `ErrFallenBehind` when data is unavailable.
- **Journal at broker level**: records reassembled frames (not raw CAN fragments), tapped via non-blocking channel send after fan-out. See `docs/format.md` for the `.lpj` binary format spec.
- **Block-level zstd compression**: journal blocks are compressed individually with zstd (default enabled, ~4x ratio at 256KB blocks). Each compressed block has a 12-byte header (BaseTime + CompressedLen). A block index at EOF provides O(1) offset lookup; forward-scan fallback handles crash-truncated files.
- **Broker replica mode**: `ReplicaMode` flag makes the broker honor external sequence numbers (from replication) instead of auto-incrementing. Skips ISO requests since there's no CAN bus.

## Conventions

- Run `golangci-lint run` before pushing (CI enforces it, config in `.golangci.yml`)
- Go 1.25+, modern patterns (enhanced ServeMux routing, slog, `slices`)
- No mocks in tests, real instances only
- CAN ID is 29-bit extended (NMEA 2000 only)
- All data encodings follow NMEA 2000: little-endian, 0xFF padding, fast-packet protocol
- Sequence numbers start at 1 (0 means "never ACK'd")
- Protobuf regeneration: `make proto` (requires `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc`)

## Configuration

lplex supports HOCON config files (`-config path` or auto-discovered from `./lplex.conf`, `/etc/lplex/lplex.conf`). CLI flags always override config file values (detected via `flag.Visit()`). Config values are applied through `flag.Set()` so they share the same parsing path as CLI flags. The mapping from HOCON paths to flag names lives in `configToFlag` in `cmd/lplex/config.go`.

lplex-cloud uses the same pattern with `lplex-cloud.conf` (auto-discovered from `./lplex-cloud.conf`, `/etc/lplex-cloud/lplex-cloud.conf`). Mapping in `cmd/lplex-cloud/config.go`.

## Dependencies

- `go.einride.tech/can` - SocketCAN bindings
- `github.com/grandcat/zeroconf` - mDNS service discovery
- `github.com/klauspost/compress` - zstd compression for journal blocks
- `github.com/gurkankaymak/hocon` - HOCON config file parser
- `google.golang.org/grpc` - gRPC framework for replication protocol
- `google.golang.org/protobuf` - Protocol Buffers runtime
