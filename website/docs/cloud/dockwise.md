---
sidebar_position: 4
title: Dockwise Cloud
---

# Dockwise Cloud

[Dockwise](https://dockwise.app) is the managed cloud offering built on lplex-cloud. It handles infrastructure, certificates, monitoring, and storage so you can focus on your boat data.

## What you get

- Managed lplex-cloud instance with automatic scaling
- mTLS certificate provisioning and rotation
- Journal storage with automatic archival to S3
- HTTP/SSE API access at `https://lplex.dockwise.app`
- Dashboard for monitoring fleet connectivity and replication status

## Connecting your boat

Dockwise provides a provisioning flow that generates boat certificates and configuration. Once provisioned, configure your boat's lplex:

```hocon
replication {
  target = "lplex.dockwise.app:443"
  instance-id = "your-boat-id"
  tls {
    cert = "/etc/lplex/dockwise.crt"
    key = "/etc/lplex/dockwise.key"
    ca = "/etc/lplex/dockwise-ca.crt"
  }
}
```

## API access

Use the same HTTP API as self-hosted lplex-cloud:

```bash
# List your instances
curl https://lplex.dockwise.app/instances

# Stream live data
curl -N https://lplex.dockwise.app/instances/your-boat-id/events

# Get devices
curl https://lplex.dockwise.app/instances/your-boat-id/devices
```

Or use the [TypeScript client](/integration/typescript-client):

```typescript
import { CloudClient } from '@sixfathoms/lplex';

const cloud = new CloudClient('https://lplex.dockwise.app');
const boatClient = cloud.client('your-boat-id');
const devices = await boatClient.devices();
```

## Self-hosted vs Dockwise

| | Self-hosted | Dockwise |
|---|---|---|
| Infrastructure | You manage | Managed |
| Certificates | You create and rotate | Automatic provisioning |
| Storage | Your disk/S3 | Managed S3 |
| Monitoring | Your Prometheus/Grafana | Built-in dashboard |
| Cost | Your compute + storage | Subscription |
| Control | Full | API access |
