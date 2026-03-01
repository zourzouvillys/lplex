import { parseArgs } from "node:util";
import { hostname } from "node:os";
import { Client, type Device, type Event, type Filter } from "@sixfathoms/lplex";
import { discover } from "./discover.js";
import {
  formatFrame,
  printDeviceTable,
  type DeviceMap,
} from "./display.js";

// ---------------------------------------------------------------------------
// CLI argument parsing
// ---------------------------------------------------------------------------

const { values } = parseArgs({
  options: {
    server: { type: "string", short: "s" },
    "client-id": { type: "string" },
    "buffer-timeout": { type: "string" },
    reconnect: { type: "boolean", default: true },
    "no-reconnect": { type: "boolean", default: false },
    "reconnect-delay": { type: "string", default: "2" },
    "ack-interval": { type: "string", default: "5" },
    quiet: { type: "boolean", short: "q", default: false },
    json: { type: "boolean", default: false },
    version: { type: "boolean", short: "v", default: false },
    pgn: { type: "string", multiple: true },
    manufacturer: { type: "string", multiple: true },
    instance: { type: "string", multiple: true },
    name: { type: "string", multiple: true },
    help: { type: "boolean", short: "h", default: false },
  },
  strict: true,
});

if (values.help) {
  process.stderr.write(`lplex-cli - NMEA 2000 CAN bus stream viewer

Usage: lplex-cli [options]

Connection:
  -s, --server <url>          lplex server URL (auto-discovered via mDNS if omitted)
  --client-id <id>            session client ID (defaults to hostname)
  --buffer-timeout <duration> ISO 8601 duration (e.g. PT5M) to enable buffered mode
  --no-reconnect              disable auto-reconnect on disconnect
  --reconnect-delay <secs>    seconds between reconnect attempts (default: 2)
  --ack-interval <secs>       seconds between ACKs in buffered mode (default: 5)

Filters (categories AND'd, values within a category OR'd):
  --pgn <number>              filter by PGN (repeatable)
  --manufacturer <name>       filter by manufacturer name or code (repeatable)
  --instance <number>         filter by device instance (repeatable)
  --name <hex>                filter by 64-bit CAN NAME in hex (repeatable)

Output:
  -q, --quiet                 suppress status messages on stderr
  --json                      force JSON output (auto-enabled when stdout is piped)

Other:
  -v, --version               print version and exit
  -h, --help                  show this help
`);
  process.exit(0);
}

if (values.version) {
  process.stderr.write("lplex-cli 0.1.0\n");
  process.exit(0);
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const quiet = values.quiet!;
const jsonMode = values.json || !process.stdout.isTTY;
const reconnect = values.reconnect && !values["no-reconnect"];
const reconnectDelay = parseFloat(values["reconnect-delay"]!) * 1000;
const ackInterval = parseFloat(values["ack-interval"]!) * 1000;
const bufferTimeout = values["buffer-timeout"];
const clientId = values["client-id"] || hostname() || "lplex-cli";

// Build filter from CLI flags.
const filter: Filter = {};
if (values.pgn?.length) filter.pgn = values.pgn.map(Number);
if (values.manufacturer?.length) filter.manufacturer = values.manufacturer;
if (values.instance?.length) filter.instance = values.instance.map(Number);
if (values.name?.length) filter.name = values.name;

const filterIsEmpty =
  !filter.pgn?.length &&
  !filter.manufacturer?.length &&
  !filter.instance?.length &&
  !filter.name?.length;

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

function log(msg: string): void {
  if (quiet) return;
  const now = new Date();
  const hh = String(now.getHours()).padStart(2, "0");
  const mm = String(now.getMinutes()).padStart(2, "0");
  const ss = String(now.getSeconds()).padStart(2, "0");
  process.stderr.write(`[${hh}:${mm}:${ss}] ${msg}\n`);
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

function writeFrame(f: Event & { type: "frame" }, devices: DeviceMap): void {
  if (jsonMode) {
    process.stdout.write(JSON.stringify(f.frame) + "\n");
  } else {
    process.stdout.write(formatFrame(f.frame, devices) + "\n");
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

let aborted = false;
const ac = new AbortController();

process.on("SIGINT", () => {
  aborted = true;
  ac.abort();
});
process.on("SIGTERM", () => {
  aborted = true;
  ac.abort();
});

async function run(): Promise<void> {
  // Resolve server URL.
  let serverURL = values.server;
  if (!serverURL) {
    log("discovering lplex via mDNS...");
    try {
      serverURL = await discover();
      log(`discovered lplex at ${serverURL}`);
    } catch (err) {
      process.stderr.write(
        `error: ${err instanceof Error ? err.message : err}\n`,
      );
      process.exit(1);
    }
  }

  const client = new Client(serverURL);
  const devices: DeviceMap = new Map();

  // Reconnect loop.
  while (!aborted) {
    try {
      if (bufferTimeout) {
        await runBuffered(client, devices);
      } else {
        await runEphemeral(client, devices);
      }
    } catch (err) {
      if (aborted) break;
      log(`disconnected: ${err instanceof Error ? err.message : err}`);
      if (!reconnect) process.exit(1);
      log(`reconnecting in ${reconnectDelay / 1000}s`);
      await sleep(reconnectDelay);
    }
  }
}

async function runEphemeral(
  client: Client,
  devices: DeviceMap,
): Promise<void> {
  // Fetch initial device list.
  try {
    const devList = await client.devices(ac.signal);
    for (const d of devList) devices.set(d.src, d);
    if (!jsonMode && devList.length > 0) {
      process.stderr.write(printDeviceTable(devList));
    }
  } catch (err) {
    log(`devices: ${err instanceof Error ? err.message : err}`);
  }

  log("streaming (ephemeral)");
  const f = filterIsEmpty ? undefined : filter;
  const stream = await client.subscribe(f, ac.signal);

  for await (const event of stream) {
    if (aborted) break;
    if (event.type === "device") {
      handleDevice(event.device, devices);
    } else {
      writeFrame(event, devices);
    }
  }
}

async function runBuffered(
  client: Client,
  devices: DeviceMap,
): Promise<void> {
  const session = await client.createSession(
    {
      clientId,
      bufferTimeout: bufferTimeout!,
      filter: filterIsEmpty ? undefined : filter,
    },
    ac.signal,
  );

  const info = session.info;
  log(`session "${info.client_id}": head=${info.seq} cursor=${info.cursor}`);

  for (const d of info.devices) devices.set(d.src, d);
  if (!jsonMode && info.devices.length > 0) {
    process.stderr.write(printDeviceTable(info.devices));
  }

  log(`streaming (buffered, session=${info.client_id})`);
  const stream = await session.subscribe(ac.signal);

  // Periodic ACK.
  let lastSeq = 0;
  const ackTimer = setInterval(async () => {
    if (lastSeq > session.lastAckedSeq) {
      try {
        await session.ack(lastSeq);
        log(`ack seq=${lastSeq}`);
      } catch {
        // will retry next interval
      }
    }
  }, ackInterval);

  try {
    for await (const event of stream) {
      if (aborted) break;
      if (event.type === "device") {
        handleDevice(event.device, devices);
      } else {
        lastSeq = event.frame.seq;
        writeFrame(event, devices);
      }
    }
  } finally {
    clearInterval(ackTimer);

    // Final ACK before exit.
    if (lastSeq > session.lastAckedSeq) {
      try {
        await session.ack(lastSeq);
        log(`ack seq=${lastSeq}`);
      } catch {
        // best effort
      }
    }
    log("bye");
  }
}

function handleDevice(device: Device, devices: DeviceMap): void {
  devices.set(device.src, device);
  log(
    `device discovered: ${device.manufacturer || `src=${device.src}`} at src=${device.src}`,
  );
  if (!jsonMode) {
    process.stderr.write(printDeviceTable([...devices.values()]));
  }
}

run().catch((err) => {
  if (!aborted) {
    process.stderr.write(
      `fatal: ${err instanceof Error ? err.message : err}\n`,
    );
    process.exit(1);
  }
});
