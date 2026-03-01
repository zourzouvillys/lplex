# lplex

CAN bus HTTP bridge for NMEA 2000. Reads raw CAN frames from a SocketCAN interface, reassembles fast-packets, tracks device discovery, and streams frames to clients over SSE with session management, filtering, and replay.

## Installation

### Client (lplexdump)

```bash
# Homebrew (macOS / Linux)
brew install sixfathoms/tap/lplexdump

# From source
go install github.com/sixfathoms/lplex/cmd/lplexdump@latest
```

### Server (Linux only, requires SocketCAN)

```bash
# Debian/Ubuntu (.deb includes both lplex server and lplexdump)
sudo dpkg -i lplex_*.deb
sudo systemctl start lplex

# Docker
docker run --network host --device /dev/can0 ghcr.io/sixfathoms/lplex:latest

# From source
go install github.com/sixfathoms/lplex/cmd/lplex@latest
```

Download `.deb` packages from [GitHub Releases](https://github.com/sixfathoms/lplex/releases).

### Go Client Library

```bash
go get github.com/sixfathoms/lplex/lplexc@latest
```

## Quick Start

### Server

```bash
# Start the server (requires SocketCAN interface)
lplex -interface can0 -port 8089

# With journal recording enabled
lplex -interface can0 -port 8089 -journal-dir /var/log/lplex

# Or with systemd
sudo systemctl enable --now lplex
```

### Client (lplexdump)

```bash
# Auto-discover via mDNS and stream all frames
lplexdump

# Connect to a specific server with filtering
lplexdump -server http://inuc1.local:8089 -pgn 129025 -manufacturer Garmin

# Buffered mode with automatic reconnect replay
lplexdump -server http://inuc1.local:8089 -buffer-timeout PT5M
```

### Go Client Library (`lplexc`)

```go
import "github.com/sixfathoms/lplex/lplexc"

// Auto-discover the server
addr, _ := lplexc.Discover(ctx)
client := lplexc.NewClient(addr)

// Get devices on the bus
devices, _ := client.Devices(ctx)

// Subscribe to position updates from Garmin devices
sub, _ := client.Subscribe(ctx, &lplexc.Filter{
    PGNs:          []uint32{129025},
    Manufacturers: []string{"Garmin"},
})
defer sub.Close()

for {
    ev, err := sub.Next()
    if err != nil {
        break
    }
    fmt.Printf("Position: src=%d data=%s\n", ev.Frame.Src, ev.Frame.Data)
}
```

## Architecture

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
    |  appends pre-serialized JSON to ring buffer (64k entries)
    |  updates device registry (PGN 60928, PGN 126996)
    |  fans out to sessions and ephemeral subscribers
    |  sends ISO requests to discover new devices
    |  feeds journal writer (if enabled)
    |
    +---> ring buffer (pre-serialized JSON, power-of-2)
    +---> DeviceRegistry (keyed by source address)
    +---> sessions map (buffered clients with cursors)
    +---> subscribers map (ephemeral clients, no state)
    +---> journal chan (optional, 16k buffer)
    |
    v
HTTP Server (:8089)                JournalWriter goroutine
    |                                   |  block-based .lpj files
    +-- GET  /events                    |  zstd block compression
    +-- PUT  /clients/{id}              |  CRC32C checksums
    +-- GET  /clients/{id}/events       |  device table per block
    +-- PUT  /clients/{id}/ack          |  O(log N) time seeking
    +-- POST /send                      |  ~2-3 MB/hour at 200 fps
    +-- GET  /devices                   v
                                   .lpj journal files

CANWriter goroutine
    |  fragments fast-packets for TX
    |  writes to SocketCAN
```

## API

### Ephemeral streaming

`GET /events` with optional query params: `pgn`, `manufacturer`, `instance`, `name` (hex).

No session, no replay, no ACK. Zero server-side state after disconnect.

### Buffered sessions

1. `PUT /clients/{id}` with `{"buffer_timeout": "PT5M"}` to create/reconnect
2. `GET /clients/{id}/events` for SSE (replays from cursor, then live)
3. `PUT /clients/{id}/ack` with `{"seq": N}` to advance cursor

Disconnected sessions keep their cursor for the buffer duration.

### Transmit

`POST /send` with `{"pgn": 59904, "src": 254, "dst": 255, "prio": 6, "data": "00ee00"}`

### Devices

`GET /devices` returns JSON array of all discovered NMEA 2000 devices.

## Journal Recording

lplex can record all CAN frames to disk as block-based binary journal files (`.lpj`) for future replay and analysis.

```bash
# Enable recording (zstd compression by default)
lplex -interface can0 -journal-dir /var/log/lplex

# With rotation (new file every hour)
lplex -interface can0 -journal-dir /var/log/lplex -journal-rotate-duration PT1H

# Disable compression
lplex -interface can0 -journal-dir /var/log/lplex -journal-compression none
```

**Flags:**
| Flag | Default | Description |
|---|---|---|
| `-journal-dir` | (disabled) | Directory for journal files |
| `-journal-prefix` | `nmea2k` | Journal file name prefix |
| `-journal-block-size` | `262144` | Block size (power of 2, min 4096) |
| `-journal-compression` | `zstd` | Block compression: `none`, `zstd`, `zstd-dict` |
| `-journal-rotate-duration` | `PT1H` | Rotate after duration (ISO 8601) |
| `-journal-rotate-size` | `0` | Rotate after bytes (0 = disabled) |

Blocks are compressed individually with zstd (~4x ratio at 256KB blocks on typical CAN data, ~158 MB/day at 200 fps). Each block carries a device table so consumers can resolve source addresses without external state. A block index at end-of-file enables fast seeking; crash-truncated files are recovered via forward-scan. See [docs/format.md](docs/format.md) for the binary format specification.

## Deployment

The `.deb` package installs a systemd service that binds to `can0`. Configure via `/etc/default/lplex`:

```bash
LPLEX_ARGS="-interface can0 -port 8089 -journal-dir /var/log/lplex -journal-compression zstd"
```

## License

MIT
