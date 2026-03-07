---
sidebar_position: 2
title: Syntax Reference
---

# DSL Syntax Reference

## File structure

A `.pgn` file contains comments, enum definitions, lookup table definitions, and PGN blocks in any order.

```
# This is a comment

enum WindReference {
  0 = "true_north"
  1 = "magnetic_north"
  2 = "apparent"
}

lookup VictronRegister uint16 {
  0x0100 = "Product ID"
  0xED8D = "DC Channel 1 Voltage"
}

pgn 130306 "Wind Data" interval=100ms {
  sid              uint8          :8
  wind_speed       uint16         :16  scale=0.01   unit="m/s"
  wind_angle       uint16         :16  scale=0.0001 unit="rad"
  wind_reference   WindReference  :3
  _                                :5
}
```

## Comments

Lines starting with `#` are comments. Comments can appear anywhere.

```
# PGN 129025 — Position, Rapid Update
```

## PGN blocks

PGN definitions come in two forms:

```
# Full definition with field layout
pgn <number> "<name>" [attributes...] {
  <field definitions>
}

# Name-only definition (no field layout known)
pgn <number> "<name>" [attributes...]
```

- `number`: PGN number (decimal)
- `name`: human-readable name (becomes the Go struct name in PascalCase for full definitions)
- `attributes`: optional PGN-level metadata (see below)

### Name-only PGNs

Omitting the braces registers the PGN's name and metadata without defining a field layout. The generated `Registry` entry has `Decode: nil`.

```
pgn 129038 "AIS Class A Position Report" fast_packet
pgn 126983 "Alert" fast_packet
pgn 127493 "Transmission Parameters Dynamic"
```

Use this form when the PGN's field structure is unknown or not yet implemented. Name-only PGNs still participate in fast-packet identification, PGN name display, and interval metadata.

### PGN-level attributes

Attributes between the description and opening `{` describe transport and timing metadata for the PGN:

| Attribute | Description |
|---|---|
| `fast_packet` | Bare flag. PGN uses multi-frame fast-packet protocol (payloads > 8 bytes). |
| `interval=<duration>` | Default transmission interval. Accepts `ms` and `s` suffixes (e.g. `100ms`, `1s`, `2500ms`, `60000ms`). Stored as `time.Duration` in `PGNInfo`. |
| `on_demand` | Bare flag. PGN is event-driven (sent on request, not periodically). |
| `draft` | Bare flag. Definition is incomplete or reverse-engineered. Propagated to `PGNInfo.Draft`. |

Examples:

```
# Fast-packet PGN with 1-second interval
pgn 129029 "GNSS Position Data" fast_packet interval=1000ms {
  ...
}

# On-demand PGN (no periodic transmission)
pgn 59904 "ISO Request" on_demand {
  ...
}

# Periodic single-frame PGN
pgn 129025 "Position Rapid Update" interval=100ms {
  ...
}

# All three combined
pgn 126996 "Product Information" fast_packet on_demand interval=5000ms {
  ...
}
```

These attributes are code-generated into the `PGNInfo` struct in `pgn.Registry`:

```go
type PGNInfo struct {
    PGN         uint32
    Description string
    FastPacket  bool
    Interval    time.Duration
    OnDemand    bool
    Draft       bool
    Decode      func([]byte) (any, error)  // nil for name-only PGNs
}
```

The `FastPacket` field is used by `IsFastPacket()` to identify fast-packet PGNs at runtime. For dispatch groups (multiple PGN definitions sharing the same number), all variants must agree on PGN-level metadata.

### Field definitions

```
<name>  <type>  :<bits>  [attributes...]
```

| Component | Description |
|---|---|
| `name` | Field name (snake_case, becomes PascalCase in Go) |
| `type` | Data type (see below) |
| `:<bits>` | Bit width of the field |
| `attributes` | Optional key=value pairs |

### Padding and unknown fields

Use `_` as the field name for reserved/padding bits defined by the spec:

```
_  :5    # 5 bits of spec-defined padding
```

Use `?` for data of unknown meaning (observed non-0xFF values, but undocumented):

```
?  :32   # 32 bits of unknown data
```

Both `_` and `?` have no type and generate no Go struct field. The distinction is semantic: `_` means the spec defines these bits as reserved, `?` means "we see data here but don't know what it means."

## Data types

### Integer types

| Type | Go type | Description |
|---|---|---|
| `uint8` | `uint8` | Unsigned 8-bit (or less with `:N`) |
| `uint16` | `uint16` | Unsigned 16-bit |
| `uint32` | `uint32` | Unsigned 32-bit |
| `uint64` | `uint64` | Unsigned 64-bit |
| `int8` | `int8` | Signed 8-bit |
| `int16` | `int16` | Signed 16-bit |
| `int32` | `int32` | Signed 32-bit |
| `int64` | `int64` | Signed 64-bit |

Integer fields can use fewer bits than their type's natural width. A `uint8 :4` reads 4 bits and stores in a `uint8`.

### String type

```
model_id  string  :256   # 32 bytes (256 bits)
```

Strings are fixed-width, measured in bits (always a multiple of 8). Trailing 0xFF padding and null bytes are stripped. Use `trim="..."` to also right-trim specific characters (e.g. `trim="@ "` for AIS names that use `@` and space padding).

### Enum types

Use a previously defined enum name as the type:

```
wind_reference  WindReference  :3
```

### Lookup types

Use a previously defined lookup name as the type:

```
register_id  uint16  :16  lookup=VictronRegister
```

The `lookup=` attribute can also be used on integer fields to add a `Name()` method without changing the underlying type.

## Field attributes

These are per-field attributes (placed after the `:bits` specifier). For PGN-level attributes, see [PGN-level attributes](#pgn-level-attributes) above.

| Attribute | Value | Description |
|---|---|---|
| `scale=N` | float | Multiply raw integer by this factor. Changes Go field to `float64`. |
| `offset=N` | float | Add to scaled value: `decoded = raw * scale + offset`. |
| `unit="..."` | string | Unit annotation (informational, included in generated comments) |
| `trim="..."` | string | Right-trim these characters from decoded string (e.g. `"@ "` for AIS padding). Only valid on `string` fields. |
| `value=N` | integer | Fixed value for dispatch. Field must equal this value for the PGN to match. |
| `lookup=Name` | identifier | Attach a lookup table for `Name()` method |
| `repeat=N` | integer | Generate a slice of N elements (see [Repeated Fields](/pgn-dsl/repeated-fields)) |
| `group="map"` | string | With `repeat=`, generate a map keyed by instance index |
| `as="name"` | string | Custom name for the repeated field in the Go struct |

## Bit layout

Fields are packed in order from bit 0 (LSB of byte 0). NMEA 2000 uses little-endian byte order. The generator tracks the current bit offset and reads each field at the appropriate position.

Example for PGN 129025 (8 bytes):

```
Bit offset  0                              32
            |------- latitude (32 bits) ---|------- longitude (32 bits) ---|
Byte        0    1    2    3               4    5    6    7
```

## Null detection

NMEA 2000 uses all-bits-set as a null/unavailable sentinel. The generated decoder checks for this and uses Go zero values (or NaN for scaled floats) when the raw value indicates null.

| Type | Null value |
|---|---|
| `uint8 :8` | 0xFF |
| `uint16 :16` | 0xFFFF |
| `int16 :16` | 0x7FFF |
| Scaled float | All-bits-set in raw value |
