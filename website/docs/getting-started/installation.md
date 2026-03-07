---
sidebar_position: 1
title: Installation
---

# Installation

lplex has three binaries with different platform support:

| Binary | Linux | macOS | Notes |
|---|---|---|---|
| `lplex` (server) | amd64, arm64 | No | Requires SocketCAN |
| `lplex-cloud` | amd64, arm64 | No | Cloud receiver |
| `lplexdump` (client) | amd64, arm64 | amd64, arm64 | CLI tool only |

## Debian/Ubuntu (.deb package)

The `.deb` package bundles `lplex`, `lplex-cloud`, and `lplexdump` with a systemd unit file.

```bash
# Download the latest release
curl -LO https://github.com/sixfathoms/lplex/releases/latest/download/lplex_amd64.deb

# Install
sudo dpkg -i lplex_amd64.deb

# The service is not started automatically. Configure first:
sudo vim /etc/lplex/lplex.conf

# Then enable and start
sudo systemctl enable lplex
sudo systemctl start lplex
```

For ARM64 (Raspberry Pi, etc.):

```bash
curl -LO https://github.com/sixfathoms/lplex/releases/latest/download/lplex_arm64.deb
sudo dpkg -i lplex_arm64.deb
```

## Homebrew (lplexdump only)

The Homebrew formula installs only the `lplexdump` client. Available on macOS and Linux.

```bash
brew install sixfathoms/tap/lplexdump
```

## Docker

The Docker image runs on Linux (amd64 and arm64). It includes both `lplex` and `lplex-cloud`.

```bash
# lplex (boat server)
docker run --rm --network=host \
  --device /dev/net/tun \
  ghcr.io/sixfathoms/lplex \
  lplex -interface can0 -port 8089

# lplex-cloud
docker run --rm -p 9443:9443 -p 8080:8080 \
  -v /data/lplex:/data/lplex \
  -v /etc/lplex-cloud:/etc/lplex-cloud:ro \
  ghcr.io/sixfathoms/lplex \
  lplex-cloud -data-dir /data/lplex
```

:::note
The boat server needs `--network=host` to access the SocketCAN interface on the host.
:::

## Build from source

Requires Go 1.25 or later.

```bash
git clone https://github.com/sixfathoms/lplex.git
cd lplex

# Build all binaries
go build -o lplex ./cmd/lplex
go build -o lplex-cloud ./cmd/lplex-cloud
go build -o lplexdump ./cmd/lplexdump

# Run tests
go test ./... -v -count=1

# Lint
golangci-lint run
```

### Protobuf regeneration

Only needed if you modify `.proto` files:

```bash
# Install protoc plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

make proto
```

### PGN code generation

Only needed if you modify `.pgn` definition files:

```bash
go generate ./pgn/...
```
