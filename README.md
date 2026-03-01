# lplex

CAN bus HTTP bridge for NMEA 2000. Reads raw CAN frames from a SocketCAN interface, reassembles fast-packets, tracks device discovery, and streams frames to clients over SSE with session management, filtering, and replay.

## Installation

### From source

```bash
go install github.com/sixfathoms/lplex/cmd/lplex@latest
go install github.com/sixfathoms/lplex/cmd/lplexdump@latest
```

### Homebrew

```bash
brew install sixfathoms/tap/lplex
```

### Docker

```bash
docker run --network host --device /dev/can0 ghcr.io/sixfathoms/lplex:latest
```

### Debian/Ubuntu

Download the `.deb` from [GitHub Releases](https://github.com/sixfathoms/lplex/releases) and install:

```bash
sudo dpkg -i lplex_*.deb
sudo systemctl start lplex
```

## Quick Start

### Server

```bash
# Start the server (requires SocketCAN interface)
lplex -interface can0 -port 8089

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

### Go Client Library

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
    |
    +---> ring buffer (pre-serialized JSON, power-of-2)
    +---> DeviceRegistry (keyed by source address)
    +---> sessions map (buffered clients with cursors)
    +---> subscribers map (ephemeral clients, no state)
    |
    v
HTTP Server (:8089)
    |
    +-- GET  /events               ephemeral SSE stream
    +-- PUT  /clients/{id}         create/reconnect buffered session
    +-- GET  /clients/{id}/events  buffered SSE stream with replay
    +-- PUT  /clients/{id}/ack     advance cursor
    +-- POST /send                 transmit a CAN frame
    +-- GET  /devices              discovered device snapshot

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

## Deployment

The `.deb` package installs a systemd service that binds to `can0`. Configure via `/etc/default/lplex`:

```bash
LPLEX_ARGS="-interface can0 -port 8089"
```

## License

MIT
