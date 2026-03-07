---
sidebar_position: 2
title: Go Client
---

# Go Client Library

The `lplexc` package provides a Go client for communicating with an lplex server. It handles SSE parsing, session management, auto-reconnect, PGN decoding, and mDNS discovery.

## Install

```bash
go get github.com/sixfathoms/lplex/lplexc
```

## Quick start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/sixfathoms/lplex/lplexc"
)

func main() {
    client := lplexc.NewClient("http://inuc1.local:8089")

    // List devices
    devices, err := client.Devices(context.Background())
    if err != nil {
        log.Fatal(err)
    }
    for _, d := range devices {
        fmt.Printf("%s (%s) at src=%d\n", d.ModelID, d.Manufacturer, d.Src)
    }

    // Subscribe to all frames
    sub, err := client.Subscribe(context.Background(), nil)
    if err != nil {
        log.Fatal(err)
    }
    defer sub.Close()

    for {
        ev, err := sub.Next()
        if err != nil {
            break
        }
        if ev.Frame != nil {
            fmt.Printf("pgn=%d src=%d data=%s\n", ev.Frame.PGN, ev.Frame.Src, ev.Frame.Data)
        }
    }
}
```

## Client options

```go
client := lplexc.NewClient("http://inuc1.local:8089",
    lplexc.WithLogger(slog.Default()),
    lplexc.WithPoolSize(20),
    lplexc.WithBackoff(lplexc.BackoffConfig{
        InitialInterval: 2 * time.Second,
        MaxInterval:     1 * time.Minute,
        MaxRetries:      0, // unlimited
    }),
)
```

| Option | Description |
|---|---|
| `WithHTTPClient(c)` | Use a custom `*http.Client` |
| `WithTransport(t)` | Set a custom `http.RoundTripper` |
| `WithPoolSize(n)` | Max idle connections per host |
| `WithLogger(l)` | Structured logger (`*slog.Logger`) |
| `WithBackoff(b)` | Reconnection backoff config |

## Ephemeral subscription

`Subscribe` opens a `GET /events` SSE stream. Returns a `*Subscription` that yields `Event` values.

```go
filter := &lplexc.Filter{
    PGNs:          []uint32{129025, 130306},
    Manufacturers: []string{"Garmin"},
}

sub, err := client.Subscribe(ctx, filter)
if err != nil {
    log.Fatal(err)
}
defer sub.Close()

for {
    ev, err := sub.Next()
    if err != nil {
        break // io.EOF on stream end, or context cancellation
    }
    if ev.Frame != nil {
        fmt.Printf("seq=%d pgn=%d\n", ev.Frame.Seq, ev.Frame.PGN)
    }
}
```

## Auto-reconnecting subscription

`SubscribeReconnect` returns a channel that automatically reconnects with exponential backoff on disconnect.

```go
ch := client.SubscribeReconnect(ctx, &lplexc.Filter{
    PGNs: []uint32{129025},
})

for ev := range ch {
    if ev.Frame != nil {
        fmt.Println(ev.Frame.PGN, ev.Frame.Data)
    }
}
// Channel closes when ctx is cancelled
```

## Watch (decoded PGN stream)

`Watch` combines auto-reconnect with PGN decoding. It filters to a single PGN and decodes every frame into its typed Go struct.

```go
ch, err := client.Watch(ctx, 129025) // Position Rapid Update
if err != nil {
    log.Fatal(err)
}

for wv := range ch {
    pos := wv.Value.(pgn.PositionRapidUpdate)
    fmt.Printf("lat=%.6f lon=%.6f\n", pos.Latitude, pos.Longitude)
}
```

The PGN must be registered in `pgn.Registry`. Unknown PGNs return an error.

## Buffered sessions

Create a session for reliable delivery with replay:

```go
session, err := client.CreateSession(ctx, lplexc.SessionConfig{
    ClientID:      "my-pipeline",
    BufferTimeout: "PT5M",
    Filter: &lplexc.Filter{
        Manufacturers: []string{"Victron"},
    },
})
if err != nil {
    log.Fatal(err)
}

fmt.Printf("resuming from cursor=%d, head=%d\n", session.Info().Cursor, session.Info().Seq)

sub, err := session.Subscribe(ctx)
if err != nil {
    log.Fatal(err)
}
defer sub.Close()

lastSeq := uint64(0)
for {
    ev, err := sub.Next()
    if err != nil {
        break
    }
    if ev.Frame != nil {
        lastSeq = ev.Frame.Seq
        process(ev.Frame)
    }
}

// ACK what we processed
if lastSeq > 0 {
    _ = session.Ack(ctx, lastSeq)
}
```

### Session API

| Method | Description |
|---|---|
| `client.CreateSession(ctx, cfg)` | Create or reconnect a session |
| `session.Info()` | Get session metadata (client_id, seq, cursor, devices) |
| `session.Subscribe(ctx)` | Open SSE stream with replay from cursor |
| `session.Ack(ctx, seq)` | Advance cursor to this sequence |
| `session.LastAcked()` | Last successfully ACK'd sequence |

## Device discovery

```go
devices, err := client.Devices(ctx)
for _, d := range devices {
    fmt.Printf("src=%d %s %s (packets=%d)\n",
        d.Src, d.Manufacturer, d.ModelID, d.PacketCount)
}
```

## Send frames

```go
err := client.Send(ctx, 129025, 10, 255, 2, []byte{0x01, 0x02, 0x03})
```

## mDNS discovery

Find an lplex server on the local network:

```go
url, err := lplexc.Discover(ctx) // blocks up to 3 seconds
if err != nil {
    log.Fatal("no lplex server found on the network")
}

client := lplexc.NewClient(url)
```

## Types

```go
type Frame struct {
    Seq  uint64 `json:"seq"`
    Ts   string `json:"ts"`    // RFC 3339
    Prio uint8  `json:"prio"`
    PGN  uint32 `json:"pgn"`
    Src  uint8  `json:"src"`
    Dst  uint8  `json:"dst"`
    Data string `json:"data"`  // hex-encoded
}

type Device struct {
    Src              uint8  `json:"src"`
    Name             string `json:"name"`
    Manufacturer     string `json:"manufacturer"`
    ManufacturerCode uint16 `json:"manufacturer_code"`
    DeviceClass      uint8  `json:"device_class"`
    DeviceFunction   uint8  `json:"device_function"`
    DeviceInstance   uint8  `json:"device_instance"`
    UniqueNumber     uint32 `json:"unique_number"`
    ModelID          string `json:"model_id"`
    SoftwareVersion  string `json:"software_version"`
    ModelVersion     string `json:"model_version"`
    ModelSerial      string `json:"model_serial"`
    ProductCode      uint16 `json:"product_code"`
    FirstSeen        string `json:"first_seen"`
    LastSeen         string `json:"last_seen"`
    PacketCount      uint64 `json:"packet_count"`
    ByteCount        uint64 `json:"byte_count"`
}

type Filter struct {
    PGNs          []uint32 `json:"pgn,omitempty"`
    Manufacturers []string `json:"manufacturer,omitempty"`
    Instances     []uint8  `json:"instance,omitempty"`
    Names         []string `json:"name,omitempty"`
}

type Event struct {
    Frame  *Frame
    Device *Device
}

type WatchValue struct {
    Frame Frame
    Value any   // type-assert to the specific PGN struct
}
```
