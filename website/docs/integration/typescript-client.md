---
sidebar_position: 3
title: TypeScript Client
---

# TypeScript Client

The `@sixfathoms/lplex` package provides a TypeScript client for lplex and lplex-cloud. It works in browsers and Node.js.

## Install

```bash
npm install @sixfathoms/lplex
```

## Quick start

```typescript
import { Client } from '@sixfathoms/lplex';

const client = new Client('http://inuc1.local:8089');

// List devices
const devices = await client.devices();
for (const d of devices) {
  console.log(`${d.model_id} (${d.manufacturer}) at src=${d.src}`);
}

// Subscribe to frames
const stream = await client.subscribe();
for await (const event of stream) {
  console.log(`pgn=${event.pgn} src=${event.src} data=${event.data}`);
}
```

## Client API

### Constructor

```typescript
const client = new Client(baseUrl: string);
```

### Devices

```typescript
const devices: Device[] = await client.devices(signal?: AbortSignal);
```

### Values

```typescript
const values = await client.values(filter?: Filter, signal?: AbortSignal);
```

### Subscribe (ephemeral)

Returns an async iterable of frames from `GET /events`.

```typescript
const stream = await client.subscribe(filter?: Filter, signal?: AbortSignal);

for await (const frame of stream) {
  console.log(frame.pgn, frame.data);
}
```

### Send

```typescript
await client.send({
  pgn: 129025,
  src: 10,
  dst: 255,
  prio: 2,
  data: '0102030405060708',
}, signal?: AbortSignal);
```

### Buffered sessions

```typescript
const session = await client.createSession({
  clientId: 'my-dashboard',
  bufferTimeout: 'PT5M',
  filter: { pgn: [129025, 130306] },
}, signal?: AbortSignal);

// Stream with replay from cursor
const stream = await session.subscribe(signal?: AbortSignal);
for await (const frame of stream) {
  console.log(frame.seq, frame.pgn);
}

// ACK processed frames
await session.ack(lastSeq, signal?: AbortSignal);
```

## CloudClient API

For connecting to `lplex-cloud` and accessing per-instance data.

```typescript
import { CloudClient } from '@sixfathoms/lplex';

const cloud = new CloudClient('https://lplex.dockwise.app');

// List all boat instances
const instances = await cloud.instances();
for (const inst of instances) {
  console.log(`${inst.id}: connected=${inst.connected}, lag=${inst.lag_seqs}`);
}

// Get status for a specific instance
const status = await cloud.status('boat-001');
console.log(`cursor=${status.cursor}, holes=${status.holes.length}`);

// Get replication diagnostic events
const events = await cloud.replicationEvents('boat-001', 100);
for (const ev of events) {
  console.log(`${ev.time}: ${ev.type} ${ev.detail}`);
}

// Get a Client scoped to an instance (same API as boat-side Client)
const boatClient = cloud.client('boat-001');
const devices = await boatClient.devices();
const stream = await boatClient.subscribe({ pgn: [129025] });
```

## Types

```typescript
interface Frame {
  seq: number;
  ts: string;      // RFC 3339
  prio: number;
  pgn: number;
  src: number;
  dst: number;
  data: string;    // hex-encoded
}

interface Device {
  src: number;
  name: string;
  manufacturer: string;
  manufacturer_code: number;
  device_class: number;
  device_function: number;
  device_instance: number;
  unique_number: number;
  model_id: string;
  software_version: string;
  model_version: string;
  model_serial: string;
  product_code: number;
  first_seen: string;
  last_seen: string;
  packet_count: number;
  byte_count: number;
}

interface Filter {
  pgn?: number[];
  manufacturer?: string[];
  instance?: number[];
  name?: string[];
}

interface SessionConfig {
  clientId: string;
  bufferTimeout: string;   // ISO 8601
  filter?: Filter;
}
```

## Browser usage

The client uses `fetch` and `EventSource` internally, so it works in any modern browser.

```html
<script type="module">
import { Client } from '@sixfathoms/lplex';

const client = new Client('http://inuc1.local:8089');
const stream = await client.subscribe({ pgn: [129025] });

for await (const frame of stream) {
  document.getElementById('position').textContent =
    `PGN ${frame.pgn}: ${frame.data}`;
}
</script>
```

## Node.js usage

Works with Node.js 18+ (native fetch and ReadableStream support).

```typescript
import { Client } from '@sixfathoms/lplex';

const client = new Client('http://inuc1.local:8089');
const devices = await client.devices();
console.log(devices);
```
