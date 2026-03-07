---
sidebar_position: 2
title: Configuration
---

# Configuration

Both `lplex` and `lplex-cloud` support [HOCON](https://github.com/lightbend/config/blob/main/HOCON.md) configuration files. CLI flags always override config file values.

## Config file discovery

**lplex** looks for config files in this order:
1. Path specified with `-config /path/to/file`
2. `./lplex.conf` (current directory)
3. `/etc/lplex/lplex.conf`

**lplex-cloud** uses the same pattern:
1. `-config /path/to/file`
2. `./lplex-cloud.conf`
3. `/etc/lplex-cloud/lplex-cloud.conf`

## lplex (boat server)

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

health {
  # Health check reports unhealthy after this silence duration
  bus-silence-threshold = PT30S
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

replication {
  # Cloud server gRPC address (empty = disabled)
  target = "cloud.example.com:9443"

  # Instance identifier (must match mTLS cert CN)
  instance-id = boat-001

  tls {
    # Client certificate for mTLS
    cert = /etc/lplex/client.crt

    # Client private key
    key = /etc/lplex/client.key

    # CA certificate to verify cloud server
    ca = /etc/lplex/ca.crt
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

# Same retention/archive config as lplex
journal {
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

Retention and archive flags are the same as lplex (see table above).

## Systemd

The `.deb` package installs a systemd unit at `/lib/systemd/system/lplex.service`. You can override settings via environment variables in `/etc/default/lplex`:

```bash
# /etc/default/lplex
LPLEX_ARGS="-interface can0 -port 8089"
```

Or use the config file at `/etc/lplex/lplex.conf` (preferred).

```bash
# Check service status
sudo systemctl status lplex

# View logs
sudo journalctl -u lplex -f

# Restart after config changes
sudo systemctl restart lplex
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
