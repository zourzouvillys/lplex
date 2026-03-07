---
sidebar_position: 1
title: Overview
---

# PGN DSL Overview

lplex includes a domain-specific language (DSL) for defining NMEA 2000 Parameter Group Numbers (PGNs). The DSL compiles into Go structs with `Decode` and `Encode` methods, Protobuf message definitions, and JSON Schema.

## Why a DSL?

NMEA 2000 PGNs are bit-packed binary structures. Manually writing decoders is tedious, error-prone, and hard to maintain. The DSL lets you write a concise definition:

```
pgn 129025 "Position Rapid Update" interval=100ms {
  latitude   int32  :32  scale=1e-7 unit="deg"
  longitude  int32  :32  scale=1e-7 unit="deg"
}
```

And generates:

- A Go struct (`PositionRapidUpdate`) with typed fields
- A `DecodePositionRapidUpdate([]byte)` function that reads bits, applies scaling, handles null values
- An `Encode() []byte` method for the reverse direction
- A `pgn.Registry` entry with metadata (description, fast-packet flag, transmission interval, on-demand flag, decode function)
- Protobuf and JSON Schema definitions

## The pipeline

```
pgn/defs/*.pgn          DSL definition files
       |
   go generate ./pgn/...
       |
   cmd/pgngen            Code generator binary
       |
   pgn/generated.go     Go structs + decoders + encoders
   pgn/generated.proto   Protobuf definitions
   pgn/generated.schema.json  JSON Schema
```

## Definition files

PGN definitions live in `pgn/defs/`:

| File | Contents |
|---|---|
| `navigation.pgn` | Position, heading, speed, depth, wind reference enums |
| `engine.pgn` | Engine parameters, battery, charger, fluid level, switch bank |
| `environment.pgn` | Temperature, humidity, pressure, wind data |
| `system.pgn` | ISO address claim, product info, heartbeat, proprietary PGNs |
| `ais.pgn` | AIS position reports, static data, and related PGNs |
| `alert.pgn` | NMEA 2000 alert PGNs |

Many of these PGNs are **name-only** definitions: they register a name and metadata (fast-packet flag, interval) without defining a field layout. This is the canonical form for PGNs whose structure is unknown or not yet implemented.

## Generated code

The generated code lives in the `pgn` package alongside hand-written helpers. The registry (~120 entries) maps PGN numbers to their metadata and decode functions:

```go
info := pgn.Registry[129025]
fmt.Println(info.Description)  // "Position Rapid Update"
fmt.Println(info.Interval)     // 100ms
fmt.Println(info.FastPacket)   // false

// Name-only PGNs have Decode == nil, so always check before calling
if info.Decode != nil {
    decoded, err := info.Decode(data)
    if err != nil {
        // decode error (data too short, etc.)
    }
    pos := decoded.(pgn.PositionRapidUpdate)
    fmt.Printf("lat=%.6f lon=%.6f\n", pos.Latitude, pos.Longitude)
}
```

## What's next

- [Syntax Reference](/pgn-dsl/syntax) for the complete DSL grammar
- [Enums & Lookups](/pgn-dsl/enums-and-lookups) for named value types
- [Dispatch](/pgn-dsl/dispatch) for proprietary PGN routing
- [Tutorial](/pgn-dsl/tutorial) to add a new PGN from scratch
