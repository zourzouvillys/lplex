---
sidebar_position: 3
title: Filtering
---

# Filtering

lplex supports filtering frames by PGN, manufacturer, device instance, and CAN NAME. Filters can be applied at the API level (query parameters), in buffered sessions, and in lplexdump.

## Filter types

| Filter | Description | Example |
|---|---|---|
| PGN | NMEA 2000 Parameter Group Number | `129025` (position), `130306` (wind) |
| Manufacturer | Device manufacturer name | `Garmin`, `Victron` |
| Instance | Device instance number (0-252) | `0`, `1` |
| NAME | 64-bit ISO 11783 CAN NAME (hex) | `0x00A1B2C3D4E5F600` |

## Filter logic

- **Multiple values of the same type** are OR'd: `-pgn 129025 -pgn 129026` matches either PGN.
- **Different filter types** are AND'd: `-pgn 129025 -manufacturer Garmin` matches PGN 129025 frames only from Garmin devices.

```
(pgn=129025 OR pgn=129026) AND (manufacturer=Garmin)
```

## lplexdump filters

Use repeatable flags:

```bash
# Wind and position data from Garmin
lplexdump -pgn 129025 -pgn 130306 -manufacturer Garmin

# All data from device instance 0
lplexdump -instance 0

# Specific device by NAME
lplexdump -name 0x00A1B2C3D4E5F600
```

## HTTP API filters

### Ephemeral mode

Pass filter parameters as query strings on `GET /events`:

```bash
curl -N "http://inuc1.local:8089/events?pgn=129025&pgn=130306&manufacturer=Garmin"
```

### Buffered mode

Set the filter when creating the session:

```bash
curl -X PUT http://inuc1.local:8089/clients/myapp \
  -d '{
    "buffer_timeout": "PT5M",
    "filter": {
      "pgn": [129025, 130306],
      "manufacturer": ["Garmin"]
    }
  }'
```

### Values endpoint

Filter last-known values:

```bash
curl "http://inuc1.local:8089/values?pgn=129025&manufacturer=Garmin"
```

## Resolved filters

When a filter uses `manufacturer`, `instance`, or `name`, lplex resolves these to source addresses at the time the filter is created. This means:

- Device-based filters don't require device registry lookups during iteration
- If a device changes its source address (e.g., address claim conflict), the filter may need to be recreated
- For ephemeral connections, filters are resolved on each new connection
- For buffered sessions, filters are resolved when the session is created

## Filter object (JSON)

The filter object used in session creation and client libraries:

```json
{
  "pgn": [129025, 129026, 130306],
  "manufacturer": ["Garmin", "Victron"],
  "instance": [0, 1],
  "name": ["0x00A1B2C3D4E5F600"]
}
```

All fields are optional. An empty filter (or no filter) matches all frames.
