---
sidebar_position: 4
title: Embedding
---

# Embedding lplex

The `lplex` package can be imported as a Go library to embed the broker and HTTP API into your own application. This lets you build custom NMEA 2000 integrations without running the `lplex` binary.

## Install

```bash
go get github.com/sixfathoms/lplex
```

## Minimal example

```go
package main

import (
    "log/slog"
    "net/http"
    "time"

    "github.com/sixfathoms/lplex"
)

func main() {
    logger := slog.Default()

    // Create and start the broker
    broker := lplex.NewBroker(lplex.BrokerConfig{
        RingSize:          65536,
        MaxBufferDuration: 5 * time.Minute,
        Logger:            logger,
    })
    go broker.Run()

    // Create the HTTP server
    srv := lplex.NewServer(broker, logger)

    // Mount under a prefix
    mux := http.NewServeMux()
    mux.Handle("/nmea/", http.StripPrefix("/nmea", srv))

    // Feed frames into the broker
    go func() {
        for frame := range getFrames() {
            broker.RxFrames() <- lplex.RxFrame{
                Timestamp: time.Now(),
                Header: lplex.CANHeader{
                    Priority:    frame.Priority,
                    PGN:         frame.PGN,
                    Source:      frame.Source,
                    Destination: 0xFF,
                },
                Data: frame.Data,
            }
        }
        broker.CloseRx()
    }()

    http.ListenAndServe(":8089", mux)
}
```

## Key types

### BrokerConfig

```go
type BrokerConfig struct {
    RingSize          int           // Power of 2, default 65536
    MaxBufferDuration time.Duration // Max session buffer timeout
    Logger            *slog.Logger
    ReplicaMode       bool          // Honor external sequence numbers
    InitialHead       uint64        // Starting sequence (replica mode)
}
```

### RxFrame

```go
type RxFrame struct {
    Timestamp time.Time
    Header    CANHeader
    Data      []byte
}
```

### CANHeader

```go
type CANHeader struct {
    Priority    uint8
    PGN         uint32
    Source      uint8
    Destination uint8
}
```

## Adding journaling

```go
jw, err := lplex.NewJournalWriter(lplex.JournalConfig{
    Dir:         "/var/log/myapp/nmea",
    Prefix:      "nmea2k",
    BlockSize:   262144,
    Compression: lplex.CompressionZstd,
    RotateDuration: 1 * time.Hour,
    Logger:      logger,
})
if err != nil {
    log.Fatal(err)
}
defer jw.Close()

// Wire journal into broker
broker := lplex.NewBroker(lplex.BrokerConfig{
    RingSize:          65536,
    MaxBufferDuration: 5 * time.Minute,
    JournalCh:         jw.Ch(),
    JournalDir:        "/var/log/myapp/nmea",
    Logger:            logger,
})
go broker.Run()
go jw.Run()
```

## Broker lifecycle

1. Create broker with `NewBroker(config)`
2. Start the broker goroutine with `go broker.Run()`
3. Feed frames via `broker.RxFrames() <- frame`
4. Create HTTP server with `NewServer(broker, logger)`
5. When done, call `broker.CloseRx()` to stop the broker goroutine

The broker owns all mutable state and runs in a single goroutine. The HTTP server and consumers access shared state through the ring buffer (with RLock) and the device registry (with RWMutex).

## Replica mode

For cloud-side brokers that receive frames via replication (not from a CAN bus), enable replica mode:

```go
broker := lplex.NewBroker(lplex.BrokerConfig{
    ReplicaMode: true,
    InitialHead: lastKnownSeq,
    Logger:      logger,
})
```

In replica mode, the broker honors external sequence numbers instead of auto-incrementing, and skips ISO Request probing (since there's no CAN bus).
