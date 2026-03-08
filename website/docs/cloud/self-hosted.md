---
sidebar_position: 2
title: Self-Hosted
---

# Self-Hosting lplex-cloud

Run your own cloud server to receive data from one or more boats.

## Build

```bash
go build -o lplex-cloud ./cmd/lplex-cloud
```

Or use the Docker image:

```bash
docker pull ghcr.io/sixfathoms/lplex-cloud
```

## Network modes

lplex-cloud supports two deployment modes:

### ACME mode (recommended for public internet)

A single port handles both gRPC (mTLS for boats) and HTTP (for clients). TLS certificates are automatically provisioned via Let's Encrypt.

```hocon
listen = ":443"
acme {
  domain = "lplex.example.com"
  email = "admin@example.com"
}
tls {
  client-ca = "/etc/lplex-cloud/ca.crt"
}
data-dir = "/data/lplex"
```

### Dual-port mode (for private networks)

Separate gRPC and HTTP listeners. You provide your own TLS certificates.

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

## mTLS certificate setup

Both the server and each boat need TLS certificates signed by the same CA. The boat's certificate CN must match its `replication-instance-id`.

### Create a CA

```bash
# Generate CA key
openssl ecparam -genkey -name prime256v1 -out ca.key

# Generate CA certificate (10 years)
openssl req -new -x509 -key ca.key -out ca.crt -days 3650 \
  -subj "/CN=lplex CA"
```

### Create server certificate

```bash
# Generate server key
openssl ecparam -genkey -name prime256v1 -out server.key

# Create CSR
openssl req -new -key server.key -out server.csr \
  -subj "/CN=lplex.example.com"

# Sign with CA
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out server.crt -days 365 \
  -extfile <(echo "subjectAltName=DNS:lplex.example.com")
```

### Create boat certificate

The CN must match the instance ID:

```bash
# Generate boat key
openssl ecparam -genkey -name prime256v1 -out boat-001.key

# Create CSR (CN = instance ID)
openssl req -new -key boat-001.key -out boat-001.csr \
  -subj "/CN=boat-001"

# Sign with CA
openssl x509 -req -in boat-001.csr -CA ca.crt -CAkey ca.key \
  -CAcreateserial -out boat-001.crt -days 365
```

### Distribute certificates

| File | Goes to |
|---|---|
| `ca.crt` | Both server and boat |
| `server.crt`, `server.key` | Cloud server only |
| `boat-001.crt`, `boat-001.key` | Boat only |

## Data directory

The `-data-dir` directory stores instance state and journal files:

```
/data/lplex/
├── boat-001/
│   ├── state.json              # Replication state (cursor, holes)
│   ├── nmea2k-20260306T101500Z.lpj
│   └── nmea2k-20260306T111500Z.lpj
├── boat-002/
│   ├── state.json
│   └── ...
```

Ensure the directory exists and has appropriate permissions.

## Systemd

```ini
[Unit]
Description=lplex-cloud
After=network.target

[Service]
ExecStart=/usr/local/bin/lplex-cloud -config /etc/lplex-cloud/lplex-cloud.conf
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Journal rotation

Live journal files on the cloud side must be rotated for archival to work. Without rotation, the `on-rotate` trigger never fires and files grow indefinitely. Rotation is configured the same way as on the boat, with duration and/or size thresholds (whichever triggers first):

```hocon
journal {
  rotate-duration = PT1H         # default, rotate after 1 hour
  # rotate-size = 536870912      # optional, rotate after 512 MB
}
```

Duration-based rotation is on by default (`PT1H`), so no action is needed unless you want a different interval or want to add a size cap. Backfill files (from the backfill stream) rotate automatically when each backfill session closes.

## Retention and archival

lplex-cloud uses the same retention and archival system as lplex. A single JournalKeeper goroutine manages all instance directories.

On startup, the keeper also runs a one-time sweep to archive any `.lpj` files that are missing `.archived` markers (skipping the most recent file per directory, which may still be active). This handles the case where the process was restarted before `on-rotate` archival could complete.

See [Retention & Archival](/user-guide/retention) for configuration details. The same retention and archive flags apply.

## Monitoring

- `GET /healthz` for health checks (includes instance counts)
- `GET /metrics` for Prometheus metrics (per-instance lag, cursor, holes)
- `GET /instances/{id}/replication/events` for diagnostic event log
