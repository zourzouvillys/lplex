---
sidebar_position: 1
title: Contributing
---

# Contributing to lplex

## Prerequisites

- Go 1.25 or later
- `golangci-lint` for linting

## Build and test

```bash
# Build all binaries
go build ./...

# Build specific binaries
go build -o lplex ./cmd/lplex
go build -o lplex-cloud ./cmd/lplex-cloud
go build -o lplexdump ./cmd/lplexdump

# Run all tests
go test ./... -v -count=1

# Lint (must pass before pushing)
golangci-lint run
```

## Code generation

If you modify `.pgn` DSL files:

```bash
go generate ./pgn/...
```

If you modify `.proto` files:

```bash
# Install protoc plugins if needed
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

make proto
```

## Code style

- **No mocks in tests.** Use real instances of brokers, servers, journal writers, etc. Mocks hide bugs; real instances catch them.
- **Tests go in `mod tests {}`** at the bottom of the file when colocated with non-test code.
- Modern Go patterns: `slog` for logging, `slices` package, enhanced ServeMux routing.
- CAN data follows NMEA 2000 conventions: little-endian, 0xFF padding, fast-packet protocol.
- Sequence numbers start at 1 (0 means "never ACK'd").
- Run `golangci-lint run` before pushing. CI enforces this.

## PGN packet tests

PGN decoders have a table-driven test framework in `pgn/packets_test.go`. Each entry specifies a PGN number, hex packet data (as output by `lplexdump`), and the expected decoded struct. The framework verifies both decode and encode round-trip automatically.

To add a test from real device data:

1. Capture a frame: `lplexdump -decode -json -pgn <pgn>`
2. Copy the `data` field as `hex` and the `decoded` fields as the `want` struct
3. Append to the `packetTests` slice in `pgn/packets_test.go`

See the [PGN tutorial](/pgn-dsl/tutorial) for the full walkthrough.

## PR workflow

1. Fork and create a feature branch
2. Write code and tests
3. Run `go test ./... -v -count=1`
4. Run `golangci-lint run`
5. Open a PR with a clear description of what changed and why

## Package layout

| Package | Owns |
|---|---|
| `lplex` (root) | Broker, Server, Consumer, CANReader, CANWriter, JournalWriter, JournalKeeper, DeviceRegistry, ValueStore, FastPacketAssembler, ReplicationClient, ReplicationServer, InstanceManager, HoleTracker, BlockWriter, EventLog, filters, ring buffer |
| `cmd/lplex/` | Boat server binary |
| `cmd/lplex-cloud/` | Cloud server binary |
| `cmd/lplexdump/` | CLI client binary |
| `cmd/pgngen/` | PGN code generator binary |
| `lplexc/` | Go client library |
| `canbus/` | CAN ID parsing and ISO NAME decoding |
| `journal/` | Journal format types and reader |
| `pgn/` | Generated PGN types, decoders, and `Registry` (with metadata: fast-packet, interval, on-demand) |
| `pgngen/` | DSL parser (PGN-level attributes, enums, lookups, dispatch) and code generators (Go, Protobuf, JSON Schema) |
| `proto/replication/v1/` | Protobuf/gRPC definitions |

## Release process

Tags trigger GoReleaser via GitHub Actions:

```bash
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

This builds binaries (Linux amd64/arm64), `.deb` packages, Docker images (`ghcr.io/sixfathoms/lplex`), and pushes the Homebrew formula to `sixfathoms/homebrew-tap`.
