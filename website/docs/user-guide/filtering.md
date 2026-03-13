---
sidebar_position: 3
title: Filtering
---

# Filtering

lplex supports filtering frames by PGN, manufacturer, device instance, and CAN NAME. Filters can be applied at the API level (query parameters), in buffered sessions, and in the lplex CLI.

## Filter types

| Filter | Description | Example |
|---|---|---|
| PGN | NMEA 2000 Parameter Group Number (include) | `129025` (position), `130306` (wind) |
| Exclude PGN | NMEA 2000 Parameter Group Number (exclude) | `60928` (address claim) |
| Manufacturer | Device manufacturer name | `Garmin`, `Victron` |
| Instance | Device instance number (0-252) | `0`, `1` |
| NAME | 64-bit ISO 11783 CAN NAME (include, hex) | `0x00A1B2C3D4E5F600` |
| Exclude NAME | 64-bit ISO 11783 CAN NAME (exclude, hex) | `0x00A1B2C3D4E5F600` |

## Filter logic

- **Multiple values of the same type** are OR'd: `--pgn 129025 --pgn 129026` matches either PGN.
- **Different filter types** are AND'd: `--pgn 129025 --manufacturer Garmin` matches PGN 129025 frames only from Garmin devices.

```
(pgn=129025 OR pgn=129026) AND (manufacturer=Garmin)
```

## lplex filters

Use repeatable flags:

```bash
# Wind and position data from Garmin
lplex dump --pgn 129025 --pgn 130306 --manufacturer Garmin

# Everything except address claims and product info
lplex dump --exclude-pgn 60928 --exclude-pgn 126996

# All data from device instance 0
lplex dump --instance 0

# Specific device by NAME
lplex dump --name 0x00A1B2C3D4E5F600

# Exclude a specific device by NAME
lplex dump --exclude-name 00A1B2C3D4E5F600
```

Exclude filters (`--exclude-pgn`, `--exclude-name`) can also be set in the [config file](./lplex#config-file) at both the global and per-boat level. Config, per-boat, and CLI exclusions are all additive.

## HTTP API filters

### Ephemeral mode

Pass filter parameters as query strings on `GET /events`:

```bash
curl -N "http://inuc1.local:8089/events?pgn=129025&pgn=130306&manufacturer=Garmin"

# Exclude specific PGNs
curl -N "http://inuc1.local:8089/events?exclude_pgn=60928&exclude_pgn=126996"

# Exclude a specific device by CAN NAME
curl -N "http://inuc1.local:8089/events?exclude_name=00A1B2C3D4E5F600"
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
  "name": ["00A1B2C3D4E5F600"],
  "exclude_name": ["00DEADBEEFCAFE00"]
}
```

All fields are optional. An empty filter (or no filter) matches all frames. `pgn`/`exclude_pgn` and `name`/`exclude_name` can be combined: include is checked first, then exclude.

## Display filter expressions

For field-level filtering on decoded PGN values, use lplex's `--where` flag:

```bash
# Only frames where water temperature is below 280K
lplex dump --where "pgn == 130310 && water_temperature < 280"

# Filter by lookup name
lplex dump --where 'register.name == "State of Charge"'

# Filter by destination device manufacturer
lplex dump --where 'dst.manufacturer == "Garmin"'

# Filter by source device model
lplex dump --where 'src.model_id == "GPS 19x NMEA 2000"'
```

`--where` is a client-side display filter that evaluates after all other filters (PGN, manufacturer, etc.) have been applied. It automatically enables `--decode`. Device sub-accessors (`src.manufacturer`, `dst.manufacturer`, `src.model_id`, `dst.model_id`) resolve the frame's source/destination address against the device registry. See [lplex: Display filter expressions](/user-guide/lplex#display-filter-expressions) for the full syntax.

## Where filtering happens

- **Server-side**: filters are applied on the server (query params for ephemeral, session filter for buffered). This reduces bandwidth since excluded frames are never sent.
- **Client-side**: lplex also applies PGN include/exclude filters locally before displaying frames. This acts as a safety net when the server is an older version that doesn't support all filter parameters. The `--where` display filter runs after client-side PGN filters.
- **Journal replay**: all filtering is client-side since there is no server involved.
