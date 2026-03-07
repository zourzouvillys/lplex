---
sidebar_position: 4
title: Value-Based Dispatch
---

# Value-Based Dispatch

Some PGN numbers are shared across multiple manufacturers (notably PGN 61184, the proprietary single-frame PGN). The DSL supports **value-based dispatch** to route to different decoders based on a field value.

## How it works

When multiple PGN definitions share the same PGN number, the code generator creates a dispatch function that reads the discriminator field(s) and routes to the correct decoder.

## Example: Proprietary PGN 61184

PGN 61184 is the NMEA 2000 proprietary single-frame PGN. Every manufacturer uses it differently, identified by the 11-bit manufacturer code in the first bytes.

```
# Victron (manufacturer_code = 358)
pgn 61184 "Victron Battery Register" {
  manufacturer_code  uint16  :11  value=358
  _                          :2
  industry_code      uint8   :3
  register           uint16  :16  lookup=VictronRegister
  payload            uint32  :32
}
```

The `value=358` attribute on `manufacturer_code` tells the code generator this definition only applies when that field equals 358 (Victron's manufacturer code).

## Generated dispatch function

For PGN 61184, the generator creates:

```go
func Decode61184(data []byte) (any, error) {
    // Read manufacturer_code from bits 0-10
    mfr := /* extract 11 bits */

    switch mfr {
    case 358:
        return DecodeVictronBatteryRegister(data)
    default:
        return nil, nil  // unknown manufacturer, not an error
    }
}
```

The dispatch function returns `(nil, nil)` for unknown discriminator values, distinguishing "not recognized" from "decode error".

## Adding a new manufacturer

To add support for another manufacturer's proprietary PGN:

```
# Garmin (manufacturer_code = 229)
pgn 61184 "Garmin Proprietary Data" {
  manufacturer_code  uint16  :11  value=229
  _                          :2
  industry_code      uint8   :3
  garmin_type        uint8   :8
  garmin_data        uint32  :32
}
```

After `go generate ./pgn/...`, the dispatch function gains a new case:

```go
switch mfr {
case 229:
    return DecodeGarminProprietaryData(data)
case 358:
    return DecodeVictronBatteryRegister(data)
default:
    return nil, nil
}
```

## Dispatch rules

- The `value=N` attribute marks a field as a discriminator
- All definitions sharing a PGN number must have the same discriminator field(s) at the same bit positions
- All variants must agree on PGN-level metadata (`fast_packet`, `interval`, `on_demand`). Conflicting metadata is a compile-time error.
- Multiple discriminator fields can be combined (e.g., manufacturer_code + sub_type)
- Unknown values return `(nil, nil)`, not an error
- The dispatch function is registered in `pgn.Registry` like any other PGN
