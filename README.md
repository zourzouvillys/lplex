# lplex

CAN bus HTTP bridge for NMEA 2000. Reads raw CAN frames from a SocketCAN interface, reassembles fast-packets, tracks device discovery, and streams frames to clients over SSE with session management, filtering, and replay. Supports cloud replication for remote access to boat data over intermittent connections.

- **Real-time SSE streaming** with [ephemeral and buffered session modes](#api), per-client filtering by PGN, manufacturer, instance, or device name
- **Fast-packet reassembly** for multi-frame NMEA 2000 PGNs, with automatic device discovery via ISO requests
- **[Journal recording](#journal-recording)** to block-based `.lpj` files with zstd compression, CRC32C checksums, and O(log N) time seeking
- **[Retention and archival](#retention-and-archival)** with max-age/min-keep/max-size knobs, soft/hard thresholds, configurable overflow policy, and pluggable archive scripts
- **[Cloud replication](#cloud-replication)** over gRPC with mTLS, live + backfill streams, hole tracking, and lazy per-instance Broker on the cloud side
- **Pull-based Consumer** with tiered replay (journal files → ring buffer → live), so clients can catch up from any point in history
- **[Embeddable core](#embedding-lplex)** as a Go package, mount the HTTP handler on any `ServeMux`
- **[Go client library](#go-client-library-lplexc)** (`lplexc`) with mDNS discovery, subscriptions, device queries, and transmit
- **CAN transmit** via [POST /send](#transmit) with automatic fast-packet fragmentation

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

### Cloud Server

```bash
# From source
go install github.com/sixfathoms/lplex/cmd/lplex-cloud@latest
```

Download `.deb` packages from [GitHub Releases](https://github.com/sixfathoms/lplex/releases).

### Go Client Library

```bash
go get github.com/sixfathoms/lplex/lplexc@latest
```

### Embedding lplex

The core package is importable, so you can embed lplex into your own service:

```bash
go get github.com/sixfathoms/lplex@latest
```

```go
import (
    "log/slog"
    "net/http"
    "time"

    "github.com/sixfathoms/lplex"
)

func main() {
    logger := slog.Default()

    // Create the broker (owns ring buffer, device registry, fan-out).
    broker := lplex.NewBroker(lplex.BrokerConfig{
        RingSize:          65536,
        MaxBufferDuration: 5 * time.Minute,
        Logger:            logger,
    })
    go broker.Run()

    // Mount the HTTP handler on a sub-path.
    srv := lplex.NewServer(broker, logger)
    mux := http.NewServeMux()
    mux.Handle("/nmea/", http.StripPrefix("/nmea", srv))

    // Feed frames from your own CAN source.
    go func() {
        for frame := range myFrameSource() {
            broker.RxFrames() <- lplex.RxFrame{
                Timestamp: frame.Time,
                Header:    lplex.CANHeader{Priority: 2, PGN: frame.PGN, Source: frame.Src, Destination: 0xFF},
                Data:      frame.Data,
            }
        }
    }()

    // Optional: enable journal recording.
    journalCh := make(chan lplex.RxFrame, 16384)
    broker.SetJournal(journalCh)
    // ... create JournalWriter and call Run in a goroutine.

    http.ListenAndServe(":8080", mux)
}
```

Lifecycle: the broker goroutine exits when you call `broker.CloseRx()`. Close the journal channel after that, then wait for the journal writer to finish.

## Quick Start

### Server

```bash
# Start the server (requires SocketCAN interface)
lplex -interface can0 -port 8089

# With a config file
lplex -config /etc/lplex/lplex.conf

# With journal recording enabled
lplex -interface can0 -port 8089 -journal-dir /var/log/lplex

# With cloud replication
lplex -interface can0 -replication-target cloud.example.com:9443 \
  -replication-instance-id boat-001 \
  -replication-tls-cert /etc/lplex/boat.crt \
  -replication-tls-key /etc/lplex/boat.key \
  -replication-tls-ca /etc/lplex/ca.crt

# Or with systemd
sudo systemctl enable --now lplex
```

### Cloud Server

```bash
# Start the cloud server with mTLS
lplex-cloud -data-dir /data/lplex \
  -tls-cert /etc/lplex-cloud/server.crt \
  -tls-key /etc/lplex-cloud/server.key \
  -tls-client-ca /etc/lplex-cloud/ca.crt

# With a config file
lplex-cloud -config /etc/lplex-cloud/lplex-cloud.conf
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

## Configuration

lplex can be configured with CLI flags, a [HOCON](https://github.com/lightbend/config/blob/main/HOCON.md) config file, or both. CLI flags always take precedence over config file values.

### Config file discovery

Use `-config path/to/lplex.conf` to specify a config file explicitly. If `-config` is not set, lplex searches for:

1. `./lplex.conf`
2. `/etc/lplex/lplex.conf`

If no config file is found, lplex continues with defaults (fully backward compatible).

### Example config (boat)

```hocon
interface = can0
port = 8089
max-buffer-duration = PT5M

journal {
  dir = /var/log/lplex
  prefix = nmea2k
  block-size = 262144
  compression = zstd

  rotate {
    duration = PT1H
    size = 0
  }

  retention {
    max-age = P30D
    min-keep = PT24H
  }

  archive {
    command = "/usr/local/bin/archive-to-s3"
    trigger = "on-rotate"
  }
}

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

### Example config (cloud)

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

journal {
  retention {
    max-age = P90D
    max-size = 53687091200
  }
  archive {
    command = "/usr/local/bin/archive-to-gcs"
    trigger = "before-expire"
  }
}
```

See [`lplex.conf.example`](lplex.conf.example) and [`lplex-cloud.conf.example`](lplex-cloud.conf.example) for the full annotated versions.

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
    +-- GET  /replication/status   .lpj journal files

CANWriter goroutine            ReplicationClient (optional)
    |  fragments for TX            |  gRPC to cloud server
    |  writes to SocketCAN         +-- Live: Consumer -> LiveFrame stream
                                   +-- Backfill: raw blocks -> Block stream
                                   +-- Reconnect: exponential backoff
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

### Replication status (boat)

`GET /replication/status` returns current replication state (available when replication is configured).

## Cloud Replication

lplex can replicate CAN bus data from a boat to a cloud instance over gRPC with mTLS. The boat initiates all connections (no public IP required). Data flows over two independent gRPC streams:

- **Live stream**: realtime frames from the broker's head, delivered to the cloud within seconds
- **Backfill stream**: raw journal blocks for filling historical gaps, newest-first

On reconnect after a connectivity gap, live data resumes immediately while backfill works through the gap in the background. The cloud runs a replica Broker per instance, so web clients connect to the cloud and get the same SSE API as if they were on the boat.

See [docs/cloud-replication.md](docs/cloud-replication.md) for the full protocol specification.

### Cloud HTTP API

| Endpoint | Description |
|---|---|
| `GET /instances` | List all instances |
| `GET /instances/{id}/status` | Instance status (cursor, holes, lag) |
| `GET /instances/{id}/events` | SSE stream from instance's broker |
| `GET /instances/{id}/devices` | Device table |
| `GET /instances/{id}/replication/events?limit=N` | Replication event log (newest first, default 100, max 1024) |

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
| `-journal-retention-max-age` | (disabled) | Delete files older than this (ISO 8601, e.g. `P30D`) |
| `-journal-retention-min-keep` | (disabled) | Never delete files younger than this, unless max-size exceeded |
| `-journal-retention-max-size` | `0` | Hard size cap in bytes; delete oldest files when exceeded |
| `-journal-retention-soft-pct` | `80` | Proactive archive threshold as % of max-size (1-99) |
| `-journal-retention-overflow-policy` | `delete-unarchived` | What to do when hard cap hit with failed archives |
| `-journal-archive-command` | (disabled) | Path to archive script |
| `-journal-archive-trigger` | (disabled) | When to archive: `on-rotate` or `before-expire` |

Blocks are compressed individually with zstd (~4x ratio at 256KB blocks on typical CAN data, ~158 MB/day at 200 fps). Each block carries a device table so consumers can resolve source addresses without external state. A block index at end-of-file enables fast seeking; crash-truncated files are recovered via forward-scan. See [docs/format.md](docs/format.md) for the binary format specification.

### Retention and Archival

Journal files accumulate indefinitely unless you configure a retention policy. Retention and archival are available on both boat and cloud binaries.

```bash
# Keep at most 30 days of journals, but never delete files less than 24 hours old
lplex -interface can0 -journal-dir /var/log/lplex \
  -journal-retention-max-age P30D -journal-retention-min-keep PT24H

# Hard size cap: keep at most 10 GB, oldest files deleted first
lplex -interface can0 -journal-dir /var/log/lplex \
  -journal-retention-max-size 10737418240

# Archive to S3 on rotation, then delete after 30 days
lplex -interface can0 -journal-dir /var/log/lplex \
  -journal-retention-max-age P30D \
  -journal-archive-command /usr/local/bin/archive-to-s3 \
  -journal-archive-trigger on-rotate
```

**Retention algorithm**: files are sorted oldest-first. Three zones govern behavior when `max-size` is set with archival:

1. **Normal** (total <= soft threshold): standard age-based expiration, archive-then-delete
2. **Soft zone** (soft < total <= hard): proactively queue oldest non-archived files for archive
3. **Hard zone** (total > hard): expire files; if archives have failed, apply the overflow policy

`max-size` overrides `min-keep` overrides `max-age`. The soft threshold defaults to 80% of `max-size` and only applies when both `max-size` and an archive command are configured.

**Overflow policies** (when hard cap is hit and archives have failed):
- `delete-unarchived` (default): delete files even if not archived, prioritizing continued recording
- `pause-recording`: stop journal writes until archives free space, prioritizing archive completeness

**Archive script protocol**: the script receives file paths as arguments and JSONL metadata on stdin (one line per file with `path`, `instance_id`, `size`, `created`). It must write JSONL to stdout with per-file status (`"ok"` or `"error"`). Failed files are retried with exponential backoff.

**Archive triggers**:
- `on-rotate`: archive immediately after a journal file is closed (eager, minimizes data loss window)
- `before-expire`: archive only when a file is about to be deleted by retention (lazy, minimizes archive traffic)

## Deployment

The `.deb` package installs a systemd service that binds to `can0`. Configure with a config file or environment variable:

```bash
# Option 1: config file (recommended)
sudo cp lplex.conf.example /etc/lplex/lplex.conf
sudo vi /etc/lplex/lplex.conf

# Option 2: environment variable
# Edit /etc/default/lplex:
LPLEX_ARGS="-interface can0 -port 8089 -journal-dir /var/log/lplex -journal-compression zstd"
```

## License

MIT
