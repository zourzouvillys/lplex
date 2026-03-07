---
sidebar_position: 7
title: Best Practices
---

# Best Practices

Production deployment tips for running lplex on a boat.

## CAN interface setup

Set up the SocketCAN interface at boot. The standard NMEA 2000 bitrate is 250 kbit/s.

```bash
# Bring up the interface
sudo ip link set can0 type can bitrate 250000
sudo ip link set can0 up

# Verify
ip -details link show can0
```

For persistent setup, create a systemd network file:

```ini
# /etc/systemd/network/80-can0.network
[Match]
Name=can0

[CAN]
BitRate=250000
```

Or use `/etc/network/interfaces` on Debian:

```
auto can0
iface can0 inet manual
    pre-up /sbin/ip link set can0 type can bitrate 250000
    up /sbin/ip link set can0 up
    down /sbin/ip link set can0 down
```

## Ring buffer sizing

The default ring buffer holds 64k entries. At a typical bus rate of ~340 frames/sec, this gives roughly 3 minutes of history. If your buffered clients need more replay window, the buffer timeout should match what the journal can provide.

The ring buffer size is fixed at compile time (power of 2). For most boats, the default is fine.

## Block size tuning

The default block size is 256 KB. Tradeoffs:

| Size | Pros | Cons |
|---|---|---|
| Smaller (64 KB) | Less data lost on crash, faster seeking | More overhead, slightly worse compression |
| Larger (1 MB) | Better compression ratio, less overhead | More data at risk on crash |

256 KB is a good balance. Only change this if you have a specific reason.

## Compression

Use `zstd` (the default). It gives ~4x compression with negligible CPU overhead. The `zstd-dict` mode offers slightly better ratios by training per-block dictionaries but uses more CPU. `none` is only useful for debugging or if you need O(1) byte-offset seeking.

## Journaling recommendations

- Always enable journaling on the boat. Disk is cheap, lost data is not.
- Set rotation to 1 hour (`PT1H`) for manageable file sizes.
- Enable retention with at least 30 days max-age.
- If replicating to cloud, set archival trigger to `on-rotate` so files are uploaded as soon as they're complete.

```hocon
journal {
  dir = /var/log/lplex
  compression = zstd
  rotate.duration = PT1H

  retention {
    max-age = P30D
    max-size = 10737418240  # 10 GB
    soft-pct = 80
    overflow-policy = delete-unarchived
  }

  archive {
    command = /usr/local/bin/archive-to-s3
    trigger = on-rotate
  }
}
```

## Monitoring bus silence

lplex alerts when no CAN frames are received for a configurable duration. This catches cable disconnects, interface failures, or power issues.

```hocon
bus-silence-timeout = PT30S
health.bus-silence-threshold = PT30S
```

The health endpoint (`GET /healthz`) reports unhealthy when the bus has been silent longer than the threshold.

## Systemd hardening

The `.deb` package includes a basic systemd unit. For production, consider adding:

```ini
[Service]
# Restrict capabilities
CapabilityBoundingSet=CAP_NET_RAW CAP_NET_ADMIN
AmbientCapabilities=CAP_NET_RAW CAP_NET_ADMIN

# Filesystem restrictions
ProtectSystem=strict
ReadWritePaths=/var/log/lplex
ProtectHome=yes

# Restart policy
Restart=always
RestartSec=5

# Resource limits
MemoryMax=256M
```

## Multiple CAN interfaces

lplex currently supports a single CAN interface per process. If you have multiple buses, run multiple instances on different ports:

```bash
lplex -interface can0 -port 8089 -journal-dir /var/log/lplex/can0
lplex -interface can1 -port 8090 -journal-dir /var/log/lplex/can1
```
