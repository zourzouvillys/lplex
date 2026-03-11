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
    Tolerances  map[string]float64         // field name -> change detection tolerance
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

### STRING_LAU type

```
alert_text  string_lau
```

`string_lau` is a variable-length Unicode string using the NMEA 2000 LAU (Length And Unicode) encoding: the first two bytes are a length prefix and encoding byte, followed by UTF-16LE encoded text. No bit width specifier is needed (the length is encoded in the data). Must be the last field in the PGN definition.

Go type: `string`. The decoder reads the length prefix, decodes UTF-16LE to UTF-8, and trims null bytes.

### Struct type

```
struct SatInfo {
  prn          uint8   :8
  elevation    int16   :16  scale=0.0001  unit="rad"
  azimuth      uint16  :16  scale=0.0001  unit="rad"
  snr          uint16  :16  scale=0.01    unit="dB"
  range_residual  int32  :32  scale=0.0001
  status       uint8   :4
  _                     :4
}

pgn 129540 "GNSS Satellites in View" fast_packet on_demand {
  sid               uint8     :8
  range_residual_mode  uint8  :2
  _                            :6
  sats_in_view      uint8     :8
  satellites        SatInfo        count=sats_in_view
}
```

`struct` defines a reusable field group for dynamically-repeated sub-records. The struct is referenced by name in a field definition with `count=<field>` to specify the repeat count at runtime. Must be the last field in the PGN definition.

Go type: the struct generates its own Go type, and the field generates a `[]StructName` slice. The decoder reads `count` elements sequentially from the remaining data.

### Bytes type

```
params  bytes                                          # raw remaining bytes
params  bytes  pgn_ref=commanded_pgn  count=num_pairs  # cross-PGN pair decode
```

`bytes` captures remaining packet data after all preceding fixed fields. No bit width specifier is needed. Must be the last field in the PGN definition.

**Without `pgn_ref`**: decodes as raw bytes. Go type: `HexBytes` (a `[]byte` that JSON-marshals as a hex string).

**With `pgn_ref=<field>` and `count=<field>`**: decodes as cross-PGN parameter pairs. The `pgn_ref` field holds the target PGN number, and `count` holds the number of pairs to decode. Each pair consists of a 1-byte field number followed by a value whose width is determined by looking up the target PGN's field metadata table.

Go type: `[]ParamPair`. JSON output:

```json
[
  {"field": 1, "name": "instance", "value": 32},
  {"field": 10, "name": "indicator_9", "value": 1}
]
```

This is used for PGN 126208 (NMEA Group Function) where Command/Request variants carry parameter pairs that reference fields in other PGNs by 1-based field number. See [Cross-PGN pair decoding](#cross-pgn-pair-decoding) below.

### Lookup types

Use a previously defined lookup name as the type:

```
register  uint16  :16  lookup=VictronRegister
```

The `lookup=` attribute adds a `Name()` method and a `LookupFields()` method without changing the underlying type. Display tools wrap lookup fields as `{"id": <raw>, "name": "..."}` objects in JSON output.

## Field attributes

These are per-field attributes (placed after the `:bits` specifier). For PGN-level attributes, see [PGN-level attributes](#pgn-level-attributes) above.

| Attribute | Value | Description |
|---|---|---|
| `scale=N` | float | Multiply raw integer by this factor. Changes Go field to `float64`. |
| `offset=N` | float | Add to scaled value: `decoded = raw * scale + offset`. |
| `unit="..."` | string | Unit annotation (informational, included in generated comments) |
| `trim="..."` | string | Right-trim these characters from decoded string (e.g. `"@ "` for AIS padding). Only valid on `string` fields. |
| `tolerance=N` | float | Change detection threshold. Fields with changes smaller than this are suppressed by the `ChangeTracker`. See [Tolerance](#tolerance-for-change-tracking). |
| `value=N` | integer | Fixed value for dispatch. Field must equal this value for the PGN to match. |
| `lookup=Name` | identifier | Attach a lookup table for `Name()` method |
| `repeat=N` | integer | Generate a slice of N elements (see [Repeated Fields](/pgn-dsl/repeated-fields)) |
| `group="map"` | string | With `repeat=`, generate a map keyed by instance index |
| `as="name"` | string | Custom name for the repeated field in the Go struct |
| `pgn_ref=field` | identifier | Target PGN field for cross-PGN pair decoding. Only valid on `bytes` fields. Requires `count=`. |
| `count=field` | identifier | Number of parameter pairs to decode. Only valid on `bytes` fields. Requires `pgn_ref=`. |

### Tolerance for change tracking

The `tolerance=` attribute sets a threshold for the `ChangeTracker` (used by `lplexdump -changes`). When a PGN has any fields with tolerances, only those fields are checked for significance. All other fields (SID counters, padding, etc.) are ignored. A field change that stays within its tolerance is suppressed; one that exceeds it triggers a delta event.

```
pgn 127257 "Attitude" interval=1000ms {
  sid    uint8   :8
  yaw    int16   :16  scale=0.0001 unit="rad" tolerance=0.01
  pitch  int16   :16  scale=0.0001 unit="rad" tolerance=0.005
  roll   int16   :16  scale=0.0001 unit="rad" tolerance=0.005
  _               :8
}
```

In this example, pitch/roll changes under 0.005 rad (~0.3 degrees) are suppressed. The `sid` field increments every packet but has no tolerance, so it's ignored entirely.

Tolerances are code-generated into `PGNInfo.Tolerances` (a `map[string]float64`) and automatically wired into the `ChangeTracker` at construction time via `FieldToleranceDiff`.

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

## Cross-PGN pair decoding

PGN 126208 (NMEA Group Function) carries parameter pairs that reference fields in a target PGN by 1-based field number. The `bytes` type with `pgn_ref=` and `count=` attributes handles this at runtime.

### How it works

The code generator builds a `fieldTable` mapping each PGN number to a `[]FieldMeta` (field name + byte width). At decode time, `decodeParamPairs()` reads each parameter pair from the raw bytes:

1. Read the 1-byte field number (1-based index into the target PGN's field list)
2. Look up the field's byte width and name from `fieldTable[targetPGN]`
3. Read that many bytes as a little-endian unsigned integer
4. Emit a `ParamPair{Field, Name, Value}`

### Example

```
pgn 126208 "NMEA Group Function Command" fast_packet on_demand {
  function_code      uint8   :8   value=1
  commanded_pgn      uint32  :24
  priority_setting   uint8   :4
  _                           :4
  number_of_pairs    uint8   :8
  params             bytes        pgn_ref=commanded_pgn  count=number_of_pairs
}
```

A Command targeting PGN 127501 (Binary Switch Bank Status) with 2 parameter pairs decodes as:

```json
{
  "function_code": 1,
  "commanded_pgn": 127501,
  "priority_setting": 8,
  "number_of_pairs": 2,
  "params": [
    {"field": 1, "name": "instance", "value": 32},
    {"field": 10, "name": "indicator_9", "value": 1}
  ]
}
```

### Field numbering

Fields in the metadata table are numbered sequentially starting at 1, matching the NMEA 2000 field numbering convention. Reserved (`_`) and unknown (`?`) fields are included in the count (with empty names) to keep indices aligned. Static repeats (`indicator :2 repeat=28`) expand to individual entries (`indicator_1` through `indicator_28`), each with their own field number.

### Acknowledge variant

The Acknowledge variant of PGN 126208 uses `bytes` without `pgn_ref`, so it decodes as raw `HexBytes`:

```
pgn 126208 "NMEA Group Function Acknowledge" fast_packet on_demand {
  function_code        uint8   :8   value=2
  acknowledged_pgn     uint32  :24
  pgn_error_code       uint8   :4
  control_error_code   uint8   :4
  number_of_pairs      uint8   :8
  params               bytes
}
```

```json
{
  "function_code": 2,
  "acknowledged_pgn": 127501,
  "params": "010a"
}
```
