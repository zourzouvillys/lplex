---
sidebar_position: 6
title: "Tutorial: Adding a PGN"
---

# Tutorial: Adding a New PGN

Walk through adding a PGN definition from scratch. We'll add PGN 127245 (Rudder) as an example.

## 1. Find the PGN specification

Look up the PGN in the NMEA 2000 appendix or a reference like canboat. PGN 127245 (Rudder) has these fields:

| Field | Type | Bits | Scale | Unit |
|---|---|---|---|---|
| Instance | uint8 | 8 | - | - |
| Direction Order | uint8 | 2 | - | - |
| Reserved | - | 6 | - | - |
| Angle Order | int16 | 16 | 0.0001 | rad |
| Position | int16 | 16 | 0.0001 | rad |
| Reserved | - | 16 | - | - |

Total: 64 bits = 8 bytes (single-frame PGN).

## 2. Write the .pgn definition

Create or edit the appropriate file in `pgn/defs/`. Rudder is a navigation PGN, so add to `navigation.pgn`:

```
# PGN 127245 — Rudder
pgn 127245 "Rudder" interval=100ms {
  instance          uint8   :8
  direction_order   uint8   :2
  _                          :6
  angle_order       int16   :16  scale=0.0001 unit="rad"
  position          int16   :16  scale=0.0001 unit="rad"
  _                          :16
}
```

Key decisions:
- `interval=100ms` because NMEA 2000 specifies 100ms default for this PGN
- Use `int16` for signed angle fields (can be negative)
- `scale=0.0001` converts raw integer to radians
- Use `_` for reserved/padding bits, `?` for data you've observed but can't identify
- Total bits must match the PGN's expected length
- For PGNs larger than 8 bytes, add `fast_packet` before the `{`
- For event-driven PGNs (no periodic TX), use `on_demand` instead of `interval=`
- If the field layout is uncertain or reverse-engineered, add `draft` before `{`

:::tip Don't know the field layout?
If you know a PGN's name but not its fields, use a **name-only definition** (no braces):

```
pgn 127245 "Rudder" interval=100ms
```

This registers the PGN in the registry with its name and metadata, but with `Decode: nil`. You can add the field layout later.
:::

## 3. Run the code generator

```bash
go generate ./pgn/...
```

This runs `cmd/pgngen` which reads all `.pgn` files and regenerates `pgn/generated.go`.

## 4. Verify the generated code

Check that the generated struct looks right:

```go
type Rudder struct {
    Instance       uint8
    DirectionOrder uint8
    AngleOrder     float64
    Position       float64
}

func DecodeRudder(data []byte) (Rudder, error) {
    // ... bit extraction, scaling, null detection
}

func (r Rudder) Encode() []byte {
    // ... reverse process
}
```

The `scale=` attribute turns `int16` fields into `float64` in the struct.

## 5. Add a packet test

Add a test entry to the `packetTests` table in `pgn/packets_test.go`. This is the standard way to verify PGN decode/encode — each entry specifies hex input, expected decoded values, and gets automatic round-trip verification.

### From lplexdump output (recommended)

The easiest way to create a test case is to capture real data from `lplexdump -decode -json`:

```bash
lplexdump -server http://inuc1.local:8089 -pgn 127245 -decode -json
```

```json
{"seq":1234,"ts":"2026-03-06T10:15:32.123Z","prio":2,"pgn":127245,"src":15,"dst":255,"data":"0001c50600000000","decoded":{"instance":0,"direction_order":1,"angle_order":0.1733,"position":0.0}}
```

Copy the `data` field as your `hex`, and use the `decoded` fields to build your `want` struct:

```go
// in pgn/packets_test.go, add to packetTests slice:
{
    desc: "rudder 10° starboard from lplexdump",
    pgn:  127245,
    hex:  "0001c50600000000",
    want: Rudder{
        Instance:       0,
        DirectionOrder: 1,
        AngleOrder:     0.1733,
        Position:       0,
    },
    epsilon: 1e-4,
},
```

### From hand-crafted data

You can also construct hex bytes manually when you want to test specific edge cases:

```go
{
    desc: "rudder -5° with direction order",
    pgn:  127245,
    hex:  "000193fd00000000",
    // direction_order = 1, angle_order = 0x1745 = 0.1745 rad ≈ 10°
    // position = 0xfd93 = -621 -> -0.0621 rad ≈ -3.6°
    want: Rudder{
        Instance:       0,
        DirectionOrder: 1,
        AngleOrder:     0.5957,
        Position:       -0.0621,
    },
    epsilon: 1e-4,
},
```

The framework runs two test functions automatically:
- **`TestPacketDecode`**: decodes hex via the Registry and compares to `want` (with float tolerance)
- **`TestPacketRoundTrip`**: encodes the decoded struct back to bytes and verifies the round-trip is stable

Set `noRoundTrip: true` for PGNs where encoding is lossy or not implemented.

## 6. Run tests

```bash
go test ./pgn/... -v -count=1 -run TestPacket
```

## 7. Verify lplexdump decoding

With the new PGN registered, `lplexdump -decode` will automatically decode PGN 127245:

```bash
lplexdump -server http://inuc1.local:8089 -pgn 127245 -decode
```

```
2026-03-06T10:15:32.123Z  seq=1234  prio=2  pgn=127245  src=15  dst=255  [8] 0001C50600000000
  {"instance":0,"direction_order":1,"angle_order":0.1733,"position":0.0}
```

## Checklist

- [ ] Find PGN specification (field types, bit widths, scaling, transport type, default interval)
- [ ] Write `.pgn` definition with appropriate PGN-level attributes (`fast_packet`, `interval=`, `on_demand`)
- [ ] Run `go generate ./pgn/...`
- [ ] Verify generated struct and decode function
- [ ] Verify `PGNInfo` metadata in registry (check `FastPacket`, `Interval`, `OnDemand`)
- [ ] Add packet test entry to `pgn/packets_test.go` (capture hex from `lplexdump -decode -json`)
- [ ] Run tests
- [ ] Run `golangci-lint run`
- [ ] Test with `lplexdump -decode` against real or simulated data
