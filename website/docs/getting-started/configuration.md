---
sidebar_position: 2
title: Configuration
---

# Configuration

Both `lplex-server` and `lplex-cloud` support [HOCON](https://github.com/lightbend/config/blob/main/HOCON.md) configuration files. CLI flags always override config file values.

## Config file discovery

**lplex-server** looks for config files in this order:
1. Path specified with `-config /path/to/file`
2. `./lplex-server.conf` (current directory)
3. `/etc/lplex-server/lplex-server.conf`

**lplex-cloud** uses the same pattern:
1. `-config /path/to/file`
2. `./lplex-cloud.conf`
3. `/etc/lplex-cloud/lplex-cloud.conf`

## lplex-server (boat server)

### Full annotated config

```hocon
# CAN interface name
interface = can0

# HTTP listen port
port = 8089

# Maximum buffer duration for buffered client sessions (ISO 8601)
max-buffer-duration = PT5M

# Alert after this duration with no CAN frames (ISO 8601)
bus-silence-timeout = PT30S

device {
  # Remove devices not seen for this duration (Go duration, 0 = disabled)
  idle-timeout = 5m
}

health {
  # Health check reports unhealthy after this silence duration
  bus-silence-threshold = PT30S
}

send {
  # Enable /send and /query HTTP endpoints (default: false).
  # With no rules, all PGNs and destinations are allowed.
  enabled = true

  # Ordered rules evaluated top-to-bottom; first match wins.
  # No matching rule = deny. Empty list + enabled = allow all.
  #
  # Rules can be strings (DSL syntax) or native HOCON objects:
  #   String:  [!] [pgn:<spec>] [name:<hex>,...]
  #   Object:  { deny = true/false, pgn = "<spec>", name = "<hex>" or [...] }
  rules = [
    "!pgn:65280-65535"                         # string: deny proprietary PGNs
    "pgn:59904"                                # string: allow ISO Request
    { pgn = "126208", name = "001c6e4000200000" }   # object: allow command to device
    { pgn = "129025-129029", name = [               # object: PGN range + device list
      "001c6e4000200000"
      "001c6e4000200001"
    ]}
  ]
}

journal {
  # Directory for .lpj journal files (empty = disabled)
  dir = /var/log/lplex

  # Filename prefix
  prefix = nmea2k

  # Block size in bytes (power of 2, min 4096)
  block-size = 262144

  # Compression: none, zstd, zstd-dict
  compression = zstd

  rotate {
    # Rotate after this duration (ISO 8601, 0 = disabled)
    duration = PT1H

    # Rotate after this many bytes (0 = disabled)
    size = 0
  }

  retention {
    # Delete files older than this (ISO 8601, 0 = disabled)
    max-age = P30D

    # Keep at least this much data even if over max-age (ISO 8601)
    min-keep = PT24H

    # Hard cap on total journal size in bytes (0 = disabled)
    max-size = 10737418240

    # Percentage of max-size for proactive archiving (0-100)
    soft-pct = 80

    # What to do when hard cap is hit and archives failed:
    # delete-unarchived or pause-recording
    overflow-policy = delete-unarchived
  }

  archive {
    # Path to archive script (empty = disabled)
    command = /usr/local/bin/archive-to-s3

    # When to archive: on-rotate or before-expire
    trigger = on-rotate
  }
}

virtual-device {
  # Claims a source address on the CAN bus so that frames sent via
  # /send and /query come from a legitimate NMEA 2000 participant.
  # Without this, some devices ignore frames from the unclaimed address 254.
  # Source address is auto-selected (highest free, counting down from 252).
  enabled = true

  # 64-bit hex ISO NAME (required). Lower values win address conflicts.
  name = "00e0170001000004"

  # Product info model ID (default: lplex-server)
  model-id = "lplex-server"

  # How often to re-broadcast address claim (PGN 60928) on the bus.
  # Keeps the bus aware we're alive and re-asserts address ownership.
  claim-heartbeat = "60s"

  # How often to re-broadcast product info (PGN 126996).
  # Longer interval since it's a 134-byte fast-packet.
  product-info-heartbeat = "5m"
}

replication {
  # Cloud server gRPC address (empty = disabled)
  target = "cloud.example.com:9443"

  # Instance identifier (must match mTLS cert CN)
  instance-id = boat-001

  tls {
    # Client certificate for mTLS
    cert = /etc/lplex-server/client.crt

    # Client private key
    key = /etc/lplex-server/client.key

    # CA certificate to verify cloud server
    ca = /etc/lplex-server/ca.crt
  }
}
```

### CLI flag reference

| Flag | HOCON Path | Default | Description |
|---|---|---|---|
| `-interface` | `interface` | `can0` | SocketCAN interface |
| `-port` | `port` | `8089` | HTTP listen port |
| `-max-buffer-duration` | `max-buffer-duration` | `PT5M` | Max buffer timeout for sessions |
| `-bus-silence-timeout` | `bus-silence-timeout` | `PT30S` | Alert on bus silence |
| `-bus-silence-threshold` | `health.bus-silence-threshold` | `PT30S` | Health check silence threshold |
| `-device-idle-timeout` | `device.idle-timeout` | `5m` | Remove devices not seen for this duration (0 = disabled) |
| `-send-enabled` | `send.enabled` | `false` | Enable /send and /query endpoints |
| `-send-rules` | `send.rules` | (empty) | Semicolon-separated send rules (HOCON: string or object array) |
| `-journal-dir` | `journal.dir` | (empty) | Journal directory |
| `-journal-prefix` | `journal.prefix` | `nmea2k` | Journal file prefix |
| `-journal-block-size` | `journal.block-size` | `262144` | Block size (bytes) |
| `-journal-compression` | `journal.compression` | `zstd` | Compression type |
| `-journal-rotate-duration` | `journal.rotate.duration` | `PT1H` | Rotation interval |
| `-journal-rotate-size` | `journal.rotate.size` | `0` | Rotation size |
| `-journal-retention-max-age` | `journal.retention.max-age` | `P30D` | Maximum file age |
| `-journal-retention-min-keep` | `journal.retention.min-keep` | `PT24H` | Minimum kept data |
| `-journal-retention-max-size` | `journal.retention.max-size` | `0` | Size hard cap |
| `-journal-retention-soft-pct` | `journal.retention.soft-pct` | `80` | Soft threshold % |
| `-journal-retention-overflow-policy` | `journal.retention.overflow-policy` | `delete-unarchived` | Overflow behavior |
| `-journal-archive-command` | `journal.archive.command` | (empty) | Archive script path |
| `-journal-archive-trigger` | `journal.archive.trigger` | `on-rotate` | Archive trigger |
| `-virtual-device` | `virtual-device.enabled` | `false` | Enable virtual NMEA 2000 device for address claiming |
| `-virtual-device-name` | `virtual-device.name` | (empty) | 64-bit hex ISO NAME (required when enabled) |
| `-virtual-device-model-id` | `virtual-device.model-id` | `lplex-server` | Product info model ID |
| `-virtual-device-claim-heartbeat` | `virtual-device.claim-heartbeat` | `60s` | Address claim re-broadcast interval |
| `-virtual-device-product-info-heartbeat` | `virtual-device.product-info-heartbeat` | `5m` | Product info re-broadcast interval |
| `-replication-target` | `replication.target` | (empty) | Cloud gRPC address |
| `-replication-instance-id` | `replication.instance-id` | (empty) | Instance ID |
| `-replication-tls-cert` | `replication.tls.cert` | (empty) | Client TLS cert |
| `-replication-tls-key` | `replication.tls.key` | (empty) | Client TLS key |
| `-replication-tls-ca` | `replication.tls.ca` | (empty) | CA cert |

## lplex-cloud

### Full annotated config

```hocon
# --- ACME mode (recommended for public internet) ---
# Single port with automatic Let's Encrypt certificates
listen = ":443"
acme {
  domain = "lplex.dockwise.app"
  email = "admin@example.com"
}
tls {
  # CA cert for verifying client mTLS certificates
  client-ca = "/etc/lplex-cloud/ca.crt"
}

# --- OR: Dual-port mode (for private networks) ---
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

# Instance state and journal storage
data-dir = "/data/lplex"

# Same retention/archive config as lplex-server
journal {
  # Rotate live journal files after this duration or size (whichever comes first).
  # Required for on-rotate archival to work (files must rotate to trigger archival).
  rotate-duration = PT1H
  # rotate-size = 0   # bytes, 0 = disabled

  retention {
    max-age = P90D
    max-size = 107374182400
    soft-pct = 80
    overflow-policy = delete-unarchived
  }
  archive {
    command = /usr/local/bin/archive-to-s3
    trigger = on-rotate
  }
}
```

### Cloud CLI flag reference

| Flag | HOCON Path | Default | Description |
|---|---|---|---|
| `-listen` | `listen` | `:443` | ACME mode listen address |
| `-acme-domain` | `acme.domain` | (empty) | Let's Encrypt domain |
| `-acme-email` | `acme.email` | (empty) | ACME account email |
| `-grpc-listen` | `grpc.listen` | `:9443` | gRPC listen (dual-port mode) |
| `-http-listen` | `http.listen` | `:8080` | HTTP listen (dual-port mode) |
| `-tls-cert` | `grpc.tls.cert` | (empty) | Server TLS cert |
| `-tls-key` | `grpc.tls.key` | (empty) | Server TLS key |
| `-tls-client-ca` | `grpc.tls.client-ca` / `tls.client-ca` | (empty) | Client CA cert |
| `-data-dir` | `data-dir` | `/data/lplex` | Data directory |

| `-device-idle-timeout` | `device.idle-timeout` | `5m` | Remove devices not seen for this duration (0 = disabled) |
| `-journal-rotate-duration` | `journal.rotate-duration` | `PT1H` | Rotate live journal files after this duration (ISO 8601) |
| `-journal-rotate-size` | `journal.rotate-size` | `0` | Rotate live journal files after this many bytes (0 = disabled) |

Retention and archive flags are the same as lplex-server (see table above).

## Systemd

The `.deb` package installs a systemd unit at `/lib/systemd/system/lplex-server.service`. You can override settings via environment variables in `/etc/default/lplex-server`:

```bash
# /etc/default/lplex-server
LPLEX_ARGS="-interface can0 -port 8089"
```

Or use the config file at `/etc/lplex-server/lplex-server.conf` (preferred).

```bash
# Check service status
sudo systemctl status lplex-server

# View logs
sudo journalctl -u lplex-server -f

# Restart after config changes
sudo systemctl restart lplex-server
```

## Duration format

All duration values use [ISO 8601 duration format](https://en.wikipedia.org/wiki/ISO_8601#Durations):

| Example | Meaning |
|---|---|
| `PT30S` | 30 seconds |
| `PT5M` | 5 minutes |
| `PT1H` | 1 hour |
| `PT24H` | 24 hours |
| `P1D` | 1 day |
| `P30D` | 30 days |
| `P90D` | 90 days |
