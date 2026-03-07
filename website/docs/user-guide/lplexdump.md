---
sidebar_position: 1
title: lplexdump
---

# lplexdump

`lplexdump` is the CLI client for lplex. It connects to a running server (or reads journal files) and displays NMEA 2000 frames with optional PGN decoding.

## Basic usage

```bash
# Auto-discover server via mDNS
lplexdump

# Specify server
lplexdump -server http://inuc1.local:8089

# With PGN decoding
lplexdump -server http://inuc1.local:8089 -decode
```

## CLI flags

| Flag | Default | Description |
|---|---|---|
| `-server` | (mDNS) | Server URL |
| `-client-id` | hostname | Session ID for buffered mode |
| `-buffer-timeout` | (empty) | Enable buffered mode with this timeout (ISO 8601) |
| `-reconnect` | `true` | Auto-reconnect on disconnect |
| `-reconnect-delay` | `2s` | Delay between reconnect attempts |
| `-ack-interval` | `5s` | How often to ACK in buffered mode |
| `-quiet` | `false` | Suppress stderr status messages |
| `-json` | `false` | Force JSON output (auto-enabled when piped) |
| `-decode` | `false` | Decode known PGNs into field values |
| `-changes` | `false` | Only show frames with changed data (suppress duplicates within tolerance) |
| `-file` | (empty) | Replay a `.lpj` journal file instead of connecting |
| `-inspect` | `false` | Inspect journal file structure and exit |
| `-speed` | `1.0` | Playback speed for journal replay (0 = max speed) |
| `-start` | (empty) | Seek to this time (RFC 3339) before playing |
| `-pgn` | (all) | Filter by PGN number (repeatable) |
| `-exclude-pgn` | (none) | Exclude specific PGN from output (repeatable) |
| `-manufacturer` | (all) | Filter by manufacturer name (repeatable) |
| `-instance` | (all) | Filter by device instance (repeatable) |
| `-name` | (all) | Filter by 64-bit CAN NAME hex (repeatable) |

## Output modes

### Terminal mode (default)

When stdout is a TTY, lplexdump shows a compact human-readable format:

```
2026-03-06T10:15:32.123Z  seq=1234  prio=2  pgn=129025  src=10  dst=255  [8] 5A1F2B3C4D5E6F70
```

With `-decode`, decoded fields appear on the next line:

```
2026-03-06T10:15:32.145Z  seq=1235  prio=3  pgn=130306  src=22  dst=255  [8] 01A4060000030000
  {"sid":1,"wind_speed":1.7,"wind_angle":0.0,"wind_reference":"apparent"}
```

### JSON mode

When stdout is piped (or `-json` is set), each frame is a JSON object per line:

```json
{"seq":1235,"ts":"2026-03-06T10:15:32.145Z","prio":3,"pgn":130306,"src":22,"dst":255,"data":"01A4060000030000","decoded":{"sid":1,"wind_speed":1.7,"wind_angle":0.0,"wind_reference":"apparent"}}
```

The `decoded` field is only present when `-decode` is enabled and the PGN is known.

Fields with `lookup=` attributes in the PGN DSL are displayed as structured objects instead of flat integers:

```json
{"register": {"id": 4095, "name": "State of Charge"}, "payload": 10000}
```

Unknown lookup values omit the `name` field: `{"register": {"id": 769}}`.

## Journal replay

Replay a recorded journal file instead of connecting to a live server:

```bash
# Normal speed playback
lplexdump -file recording.lpj

# Fast-forward at 10x
lplexdump -file recording.lpj -speed 10

# As fast as possible (0 is a special value; default is 1.0 = real-time)
lplexdump -file recording.lpj -speed 0

# Seek to a specific time
lplexdump -file recording.lpj -start 2026-03-06T10:00:00Z

# Decode during replay
lplexdump -file recording.lpj -decode

# Inspect journal structure (block layout, device table, compression)
lplexdump -file recording.lpj -inspect
```

:::note
`-file` and `-server` are mutually exclusive. Journal replay does not require a running lplex server.
:::

## Filtering

Filters can be combined. Multiple values for the same filter type are OR'd together. Different filter types are AND'd.

```bash
# GPS position and SOG/COG from Garmin devices
lplexdump -pgn 129025 -pgn 129026 -manufacturer Garmin
```

See [Filtering](/user-guide/filtering) for details.

## Change tracking

Use `-changes` to suppress duplicate frames and only show meaningful state changes. Each output frame is tagged with an event type:

- **`[snapshot]`** (green): first observation for this source+PGN pair
- **`[delta N/MB]`** (yellow): significant change detected (N = diff bytes, M = full packet bytes)
- **`[idle]`**: source stopped transmitting this PGN (timeout based on the PGN's interval)

```bash
# Live stream, only significant changes
lplexdump -server http://inuc1.local:8089 -changes -decode

# Journal replay with change tracking
lplexdump -file recording.lpj -changes -decode

# JSON output includes "change" field
lplexdump -changes -decode -json
```

"Significant" is determined by field-level tolerances declared in the PGN DSL (`tolerance=` attribute). For example, PGN 127257 (Attitude) declares `tolerance=0.005` on pitch/roll, so sub-0.005 rad sensor jitter is suppressed. PGNs without tolerances use byte-level comparison (any change is significant).

When tolerances are declared on a PGN, only the tolerance-bearing fields are checked. Other fields (like SID counters that increment every packet) are ignored.

## Piping and scripting

lplexdump is designed to work well in Unix pipelines:

```bash
# Extract just PGN numbers
lplexdump -server http://inuc1.local:8089 | jq -r .pgn

# Count frames per PGN over 10 seconds
timeout 10 lplexdump -server http://inuc1.local:8089 -json | jq .pgn | sort | uniq -c | sort -rn

# Record decoded data to file
lplexdump -server http://inuc1.local:8089 -decode > data.jsonl
```
