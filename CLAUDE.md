# lplex

CAN bus HTTP bridge for NMEA 2000. Reads raw CAN frames from a SocketCAN interface, reassembles fast-packets, tracks device discovery, and streams frames to clients over SSE with session management, filtering, and replay.

## Build & Test

```bash
go build ./...                  # build all
go build -o lplex ./cmd/lplex   # build server
go build -o lplexdump ./cmd/lplexdump
go test ./... -v -count=1       # run tests
golangci-lint run               # lint (must pass before pushing)
```

## Release

Tags trigger GoReleaser via GitHub Actions, which builds binaries, .deb, Docker images, and pushes the Homebrew formula.

```bash
git tag -a v0.2.0 -m "v0.2.0" && git push origin v0.2.0
```

**Release artifacts**:
- `lplex` server: Linux amd64/arm64 only (no macOS, needs SocketCAN)
- `lplexdump` client: Linux + macOS (amd64/arm64)
- `.deb` package: bundles both binaries + systemd unit
- Docker: `ghcr.io/sixfathoms/lplex` (linux/amd64 + linux/arm64)
- Homebrew: `sixfathoms/tap/lplexdump` (client only, pushed to `sixfathoms/homebrew-tap`)

**Secrets**: `HOMEBREW_TAP_TOKEN` (fine-grained PAT with Contents read/write on `sixfathoms/homebrew-tap`)

## Deployment

- **Host**: `inuc1.local` (Linux x86_64)
- **Install**: `.deb` package from GitHub Releases
- **Service**: `lplex.service` (systemd)
- **Config**: `/etc/default/lplex` (`LPLEX_ARGS="-interface can0 -port 8089"`)
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
    |  fans out to sessions and ephemeral subscribers (with per-client filtering)
    |  sends ISO requests to discover new devices on the bus
    |  non-blocking send to journal channel (if enabled)
    |
    +---> ring buffer ([]ringEntry, lock-free writes, RLock for replay reads)
    +---> DeviceRegistry (RWMutex, keyed by source address)
    +---> sessions map (buffered clients: channels, filters, cursors)
    +---> subscribers map (ephemeral clients: channels, filters, no state)
    +---> journal chan (16384-entry buffer, optional)
    |
    v
HTTP Server (:8089)                    JournalWriter goroutine
    |                                       |  reads from journal chan
    +-- GET  /events                        |  encodes frames into 64KB blocks
    +-- PUT  /clients/{id}                  |  writes blocks with CRC32C checksums
    +-- GET  /clients/{id}/events           |  rotates files by duration/size
    +-- PUT  /clients/{id}/ack              |  tracks device table per block
    +-- POST /send                          v
    +-- GET  /devices                  .lpj journal files

CANWriter goroutine
    |  reads from txFrames chan
    |  fragments fast-packets for TX
    |  writes to SocketCAN
```

## Package Structure

| Package | Owns |
|---|---|
| `cmd/lplex/` | Server entry point, flag parsing, wires broker + CAN reader/writer + HTTP server |
| `cmd/lplexdump/` | CLI client: SSE consumer with pretty-print, device table, auto-reconnect |
| `lplexc/` | Public Go client library: Subscribe, Devices, Send, Session, mDNS discovery |
| `canbus/` | Public CAN ID parsing (`CANHeader`, `ParseCANID`, `BuildCANID`) and ISO NAME decoding |
| `journal/` | Public journal format: `Device`, `Reader`, block constants, length-prefixed string helpers |
| `internal/server/` | Server internals (not importable externally) |

### internal/server/ File Map

| File | Owns |
|---|---|
| `broker.go` | `Broker`, `ClientSession`, `subscriber`, `EventFilter`, ring buffer, fan-out, session lifecycle, ephemeral subscriptions, journal feed |
| `server.go` | HTTP handlers, ephemeral + buffered SSE streaming, filter query param parsing, ISO 8601 duration parser |
| `can.go` | `CANReader` (SocketCAN rx + fast-packet reassembly), `CANWriter` (SocketCAN tx + fragmentation) |
| `canid.go` | Thin wrappers re-exporting `canbus.ParseCANID`, `canbus.BuildCANID` |
| `fastpacket.go` | `FastPacketAssembler`, `FragmentFastPacket`, fast-packet PGN registry |
| `devices.go` | `DeviceRegistry`, PGN 60928/126996 decoding, manufacturer lookup table |
| `journal.go` | `JournalWriter`, `JournalConfig`, block encoding, file rotation, device table tracking (with product info) |

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

## Key Design Decisions

- **Pre-serialized JSON in ring buffer**: frames are serialized once when received, not per-client.
- **Resolved filters for replay**: device-based filters are flattened to source addresses before taking the ring lock.
- **Ephemeral subscribers are separate from sessions**: clean separation, no lifecycle overhead.
- **ISO Request on unknown source**: broker discovers devices automatically.
- **Journal at broker level**: records reassembled frames (not raw CAN fragments), tapped via non-blocking channel send after fan-out. See `docs/format.md` for the `.lpj` binary format spec.

## Conventions

- Run `golangci-lint run` before pushing (CI enforces it, config in `.golangci.yml`)
- Go 1.25+, modern patterns (enhanced ServeMux routing, slog, `slices`)
- No mocks in tests, real instances only
- CAN ID is 29-bit extended (NMEA 2000 only)
- All data encodings follow NMEA 2000: little-endian, 0xFF padding, fast-packet protocol
- Sequence numbers start at 1 (0 means "never ACK'd")

## Dependencies

- `go.einride.tech/can` - SocketCAN bindings
- `github.com/grandcat/zeroconf` - mDNS service discovery
