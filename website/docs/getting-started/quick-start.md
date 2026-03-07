---
sidebar_position: 3
title: Quick Start
---

# Quick Start

This guide gets you from zero to seeing live NMEA 2000 data in a few minutes.

## Prerequisites

- A running `lplex` server connected to a CAN bus (see [Installation](/getting-started/installation))
- `lplexdump` installed on your machine

## 1. Connect and stream

If lplex is running on your local network with mDNS enabled, lplexdump will discover it automatically:

```bash
lplexdump
```

Or specify the server address directly:

```bash
lplexdump -server http://inuc1.local:8089
```

You should see frames scrolling:

```
2026-03-06T10:15:32.123Z  seq=1234  prio=2  pgn=129025  src=10  dst=255  [8] 5A1F2B3C4D5E6F70
2026-03-06T10:15:32.145Z  seq=1235  prio=3  pgn=130306  src=22  dst=255  [8] 01A4060000030000
```

## 2. Filter by PGN

Only see position reports (PGN 129025) and wind data (PGN 130306):

```bash
lplexdump -server http://inuc1.local:8089 -pgn 129025 -pgn 130306
```

## 3. Decode PGN fields

Add `-decode` to see human-readable field values:

```bash
lplexdump -server http://inuc1.local:8089 -decode
```

Output now includes decoded fields below each frame:

```
2026-03-06T10:15:32.145Z  seq=1235  prio=3  pgn=130306  src=22  dst=255  [8] 01A4060000030000
  {"sid":1,"wind_speed":1.7,"wind_angle":0.0,"wind_reference":"apparent"}
```

## 4. View connected devices

Open a browser to see all devices on the bus:

```bash
curl http://inuc1.local:8089/devices | jq
```

```json
[
  {
    "src": 10,
    "name": "0x00A1B2C3D4E5F600",
    "manufacturer": "Garmin",
    "model_id": "GPS 19x HVS",
    "packet_count": 45023,
    "byte_count": 360184
  }
]
```

## 5. Get last-known values

See the most recent frame for each device and PGN:

```bash
curl http://inuc1.local:8089/values | jq
```

Or with decoding:

```bash
curl http://inuc1.local:8089/values/decoded | jq
```

## 6. Use a buffered session

For reliable delivery with replay on reconnect:

```bash
lplexdump -server http://inuc1.local:8089 -buffer-timeout PT5M
```

This creates a server-side session that buffers up to 5 minutes of data. If lplexdump disconnects and reconnects within that window, it replays missed frames.

## 7. JSON output

Pipe to other tools with JSON output (auto-enabled when stdout is not a terminal):

```bash
lplexdump -server http://inuc1.local:8089 -decode | jq .pgn
```

Or force JSON mode explicitly:

```bash
lplexdump -server http://inuc1.local:8089 -json -decode
```

## What's next

- [Configuration](/getting-started/configuration) for all server options
- [Streaming](/user-guide/streaming) to understand ephemeral vs buffered modes
- [HTTP API](/integration/http-api) for building your own clients
- [Cloud Replication](/cloud/overview) for remote access
