---
sidebar_position: 1
title: HTTP API
---

# HTTP API Reference

lplex exposes a REST + SSE API for reading frames, managing sessions, and discovering devices. CORS is enabled (`Access-Control-Allow-Origin: *`).

## Endpoints

### Ephemeral streaming

#### `GET /events`

Opens an SSE stream of live CAN frames. No session, no replay.

**Query parameters:**

| Param | Type | Description |
|---|---|---|
| `pgn` | uint32 | Filter by PGN (repeatable, OR'd) |
| `manufacturer` | string | Filter by manufacturer name (repeatable, OR'd) |
| `instance` | uint8 | Filter by device instance (repeatable, OR'd) |
| `name` | string | Filter by 64-bit CAN NAME hex (repeatable, OR'd) |

Different filter types are AND'd together.

**Response:** `Content-Type: text/event-stream`

```
data: {"seq":1234,"ts":"2026-03-06T10:15:32.123Z","prio":2,"pgn":129025,"src":10,"dst":255,"data":"5A1F2B3C4D5E6F70"}

data: {"seq":1235,"ts":"2026-03-06T10:15:32.145Z","prio":3,"pgn":130306,"src":22,"dst":255,"data":"01A4060000030000"}

```

**Example:**

```bash
curl -N "http://inuc1.local:8089/events?pgn=129025&manufacturer=Garmin"
```

---

### Buffered sessions

#### `PUT /clients/{clientId}`

Create or reconnect a buffered session. The client ID must be 1-64 alphanumeric characters, hyphens, or underscores.

**Request body:**

```json
{
  "buffer_timeout": "PT5M",
  "filter": {
    "pgn": [129025, 130306],
    "manufacturer": ["Garmin"],
    "instance": [0],
    "name": ["0x00A1B2C3D4E5F600"]
  }
}
```

Only `buffer_timeout` is required. `filter` is optional.

**Response:** `200 OK`

```json
{
  "client_id": "myapp",
  "seq": 5000,
  "cursor": 4800,
  "devices": [
    {"src": 10, "name": "0x00A1B2C3D4E5F600", "manufacturer": "Garmin", ...}
  ]
}
```

| Field | Description |
|---|---|
| `seq` | Current head sequence number |
| `cursor` | Where this client will resume from (last ACK'd + 1) |
| `devices` | Snapshot of known devices |

---

#### `GET /clients/{clientId}/events`

Opens an SSE stream for a buffered session. Replays buffered frames from the cursor, then transitions to live streaming.

**Response:** `Content-Type: text/event-stream` (same format as `GET /events`)

Returns `404` if the session does not exist or has expired.

---

#### `PUT /clients/{clientId}/ack`

Acknowledge receipt of frames up to the given sequence number. Advances the session cursor.

**Request body:**

```json
{
  "seq": 1500
}
```

**Response:** `204 No Content`

---

### Frame transmission

#### `POST /send`

Send a CAN frame to the bus.

**Request body:**

```json
{
  "pgn": 129025,
  "src": 10,
  "dst": 255,
  "prio": 2,
  "data": "0102030405060708"
}
```

| Field | Type | Description |
|---|---|---|
| `pgn` | uint32 | PGN number |
| `src` | uint8 | Source address |
| `dst` | uint8 | Destination (255 for broadcast) |
| `prio` | uint8 | Priority (0-7, lower is higher priority) |
| `data` | string | Hex-encoded payload |

**Response:** `202 Accepted`

---

### Device discovery

#### `GET /devices`

Returns a snapshot of all discovered NMEA 2000 devices.

**Response:** `200 OK`

```json
[
  {
    "src": 10,
    "name": "0x00A1B2C3D4E5F600",
    "manufacturer": "Garmin",
    "manufacturer_code": 229,
    "device_class": 25,
    "device_function": 130,
    "device_instance": 0,
    "unique_number": 123456,
    "product_code": 1234,
    "model_id": "GPS 19x HVS",
    "software_version": "5.60",
    "model_version": "1",
    "model_serial": "ABC123",
    "first_seen": "2026-03-06T10:00:00Z",
    "last_seen": "2026-03-06T10:15:32Z",
    "packet_count": 45023,
    "byte_count": 360184
  }
]
```

---

### Last-known values

#### `GET /values`

Returns the last-seen frame for each (device, PGN) pair.

**Query parameters:** Same as `GET /events` (pgn, manufacturer, instance, name).

**Response:** `200 OK`

```json
[
  {
    "name": "0x00A1B2C3D4E5F600",
    "src": 10,
    "manufacturer": "Garmin",
    "model_id": "GPS 19x HVS",
    "values": [
      {
        "pgn": 129025,
        "ts": "2026-03-06T10:15:32.123Z",
        "data": "5A1F2B3C4D5E6F70",
        "seq": 1234
      }
    ]
  }
]
```

#### `GET /values/decoded`

Same as `/values` but with decoded PGN fields added.

**Response:** `200 OK`

```json
[
  {
    "name": "0x00A1B2C3D4E5F600",
    "src": 10,
    "manufacturer": "Garmin",
    "model_id": "GPS 19x HVS",
    "values": [
      {
        "pgn": 129025,
        "ts": "2026-03-06T10:15:32.123Z",
        "data": "5A1F2B3C4D5E6F70",
        "seq": 1234,
        "decoded": {
          "latitude": 47.6062,
          "longitude": -122.3321
        }
      }
    ]
  }
]
```

---

### Replication status

#### `GET /replication/status`

Returns replication connection and sync state. Only available when replication is configured.

**Response:** `200 OK`

```json
{
  "connected": true,
  "instance_id": "boat-001",
  "local_head_seq": 50000,
  "cloud_cursor": 49950,
  "holes": [
    {"start": 10000, "end": 10500}
  ],
  "live_lag": 50,
  "backfill_remaining_seqs": 500,
  "last_ack": "2026-03-06T10:15:30Z"
}
```

---

### Health and metrics

#### `GET /healthz`

Health check endpoint.

**Response:** `200 OK`

```json
{
  "status": "ok"
}
```

Reports unhealthy when the CAN bus has been silent longer than `bus-silence-threshold`.

#### `GET /metrics`

Prometheus metrics endpoint.

---

## Frame JSON format

Every frame in the SSE stream and API responses uses this format:

```json
{
  "seq": 1234,
  "ts": "2026-03-06T10:15:32.123Z",
  "prio": 2,
  "pgn": 129025,
  "src": 10,
  "dst": 255,
  "data": "5A1F2B3C4D5E6F70"
}
```

| Field | Type | Description |
|---|---|---|
| `seq` | uint64 | Monotonically increasing sequence number (starts at 1) |
| `ts` | string | RFC 3339 timestamp with millisecond precision |
| `prio` | uint8 | CAN priority (0-7) |
| `pgn` | uint32 | NMEA 2000 Parameter Group Number |
| `src` | uint8 | Source address (0-253) |
| `dst` | uint8 | Destination address (255 = broadcast) |
| `data` | string | Hex-encoded payload bytes |

## Cloud API

The cloud server (`lplex-cloud`) exposes a similar API namespaced by instance. See [Cloud Overview](/cloud/overview) for the full cloud endpoint reference.
