---
sidebar_position: 3
title: Filtering
---

# Filtering

lplex supports filtering frames by PGN, manufacturer, device instance, and CAN NAME. Filters can be applied at the API level (query parameters), in buffered sessions, and in lplexdump.

## Filter types

| Filter | Description | Example |
|---|---|---|
| PGN | NMEA 2000 Parameter Group Number (include) | `129025` (position), `130306` (wind) |
| Exclude PGN | NMEA 2000 Parameter Group Number (exclude) | `60928` (address claim) |
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

# Everything except address claims and product info
lplexdump -exclude-pgn 60928 -exclude-pgn 126996

# All data from device instance 0
lplexdump -instance 0

# Specific device by NAME
lplexdump -name 0x00A1B2C3D4E5F600
```

Exclude filters can also be set in the [config file](/docs/user-guide/lplexdump#config-file) at both the global and per-boat level. Config, per-boat, and CLI exclusions are all additive.

## HTTP API filters

### Ephemeral mode

Pass filter parameters as query strings on `GET /events`:

```bash
curl -N "http://inuc1.local:8089/events?pgn=129025&pgn=130306&manufacturer=Garmin"

# Exclude specific PGNs
curl -N "http://inuc1.local:8089/events?exclude_pgn=60928&exclude_pgn=126996"
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
  "exclude_pgn": [60928, 126996],
  "manufacturer": ["Garmin", "Victron"],
  "instance": [0, 1],
  "name": ["0x00A1B2C3D4E5F600"]
}
```

All fields are optional. An empty filter (or no filter) matches all frames. `pgn` (include) and `exclude_pgn` can be combined: include is checked first, then exclude.

## Display filter expressions

For field-level filtering on decoded PGN values, use lplexdump's `-where` flag:

```bash
# Only frames where water temperature is below 280K
lplexdump -where "pgn == 130310 && water_temperature < 280"

# Filter by lookup name
lplexdump -where 'register.name == "State of Charge"'
```

`-where` is a client-side display filter that evaluates after all other filters (PGN, manufacturer, etc.) have been applied. It automatically enables `-decode`. See [lplexdump: Display filter expressions](/user-guide/lplexdump#display-filter-expressions) for the full syntax.

## Where filtering happens

- **Server-side**: filters are applied on the server (query params for ephemeral, session filter for buffered). This reduces bandwidth since excluded frames are never sent.
- **Client-side**: lplexdump also applies PGN include/exclude filters locally before displaying frames. This acts as a safety net when the server is an older version that doesn't support all filter parameters. The `-where` display filter runs after client-side PGN filters.
- **Journal replay**: all filtering is client-side since there is no server involved.
