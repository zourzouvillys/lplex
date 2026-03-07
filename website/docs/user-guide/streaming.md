---
sidebar_position: 2
title: Streaming Modes
---

# Streaming Modes

lplex supports two streaming modes for receiving NMEA 2000 frames. Choose based on whether you need guaranteed delivery or just want to see live data.

## Ephemeral mode (default)

The simplest way to receive frames. Connect, get data, disconnect. No state is kept server-side.

```
Client                          lplex
  |                               |
  |--- GET /events -------------->|
  |<-- SSE: frame 1 -------------|
  |<-- SSE: frame 2 -------------|
  |    (disconnect)               |
  |                               |  (nothing stored, session gone)
  |--- GET /events -------------->|
  |<-- SSE: frame N -------------|  (starts from current, no replay)
```

**Characteristics:**
- No session ID or registration required
- Frames are pushed via Server-Sent Events (SSE)
- If you disconnect, you miss what happened while away
- No ACK mechanism
- Supports filter query parameters

**When to use:** dashboards, monitoring, debugging, any scenario where missing a few frames during reconnection is acceptable.

```bash
# CLI
lplexdump -server http://inuc1.local:8089

# curl
curl -N http://inuc1.local:8089/events
```

## Buffered mode

For reliable delivery with replay. The server keeps a cursor for your session and replays missed frames on reconnect.

```
Client                          lplex
  |                               |
  |--- PUT /clients/myapp ------->|  Create session (buffer_timeout=PT5M)
  |<-- 200 {cursor: 0} ----------|
  |                               |
  |--- GET /clients/myapp/events->|  Connect SSE
  |<-- SSE: frame 100 -----------|
  |<-- SSE: frame 101 -----------|
  |--- PUT /clients/myapp/ack --->|  ACK seq=101
  |<-- 204 ----------------------|
  |    (disconnect)               |
  |                               |  (server keeps buffering for 5 min)
  |--- GET /clients/myapp/events->|  Reconnect
  |<-- SSE: frame 102 -----------|  Replays from last ACK
  |<-- SSE: frame 103 -----------|
```

**Characteristics:**
- Register a session with `PUT /clients/{id}` before connecting
- Server tracks your cursor (last ACK'd sequence number)
- On reconnect, replays all frames since your last ACK
- Session expires after `buffer_timeout` of inactivity
- Frames are read from a tiered log: journal files (oldest), ring buffer (recent), live notifications
- Client ID must be alphanumeric with hyphens/underscores, 1-64 characters

**When to use:** data pipelines, analytics, any scenario where you cannot afford to miss frames.

```bash
# CLI (creates session automatically)
lplexdump -server http://inuc1.local:8089 -buffer-timeout PT5M

# API
curl -X PUT http://inuc1.local:8089/clients/myapp \
  -d '{"buffer_timeout":"PT5M"}'
curl -N http://inuc1.local:8089/clients/myapp/events
```

### Acknowledgment

In buffered mode, the client must periodically ACK the last processed sequence number. This tells the server it's safe to advance the cursor.

```bash
curl -X PUT http://inuc1.local:8089/clients/myapp/ack \
  -d '{"seq": 1500}'
```

`lplexdump` handles ACKs automatically (every 5 seconds by default, configurable with `-ack-interval`).

### Session expiry

If a buffered client doesn't reconnect within `buffer_timeout`, the session is cleaned up. On the next connection with the same client ID, a new session is created starting from the current head (no replay of old data).

## Consumer model

Under the hood, buffered sessions use a pull-based Consumer (similar to Kafka/Kinesis). Each consumer reads from a tiered log:

1. **Journal files** (oldest data, on disk)
2. **Ring buffer** (recent data, in memory, 64k entries)
3. **Live notification** (blocking wait for new frames)

The consumer iterates at its own pace via `Next(ctx)`. If a consumer falls too far behind and the data is no longer available in the ring buffer or journal, it receives an `ErrFallenBehind` error.

## SSE event format

Both modes use the same SSE wire format:

```
data: {"seq":1234,"ts":"2026-03-06T10:15:32.123Z","prio":2,"pgn":129025,"src":10,"dst":255,"data":"5A1F2B3C4D5E6F70"}

data: {"seq":1235,"ts":"2026-03-06T10:15:32.145Z","prio":3,"pgn":130306,"src":22,"dst":255,"data":"01A4060000030000"}

```

Each `data:` line contains a JSON-encoded frame. Events are separated by a blank line per the SSE spec.
