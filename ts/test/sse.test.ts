import { describe, it, expect } from "vitest";
import { parseSSE } from "../src/sse.js";

function makeStream(text: string): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      controller.enqueue(encoder.encode(text));
      controller.close();
    },
  });
}

// Simulate chunked delivery: each string becomes a separate chunk.
function makeChunkedStream(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
}

const sampleFrame = {
  seq: 42,
  ts: "2026-02-28T12:00:00Z",
  prio: 6,
  pgn: 129025,
  src: 1,
  dst: 255,
  data: "00aabbcc",
};

const sampleDevice = {
  type: "device",
  src: 1,
  name: "0x00deadbeef123456",
  manufacturer: "Garmin",
  manufacturer_code: 229,
  device_class: 25,
  device_function: 130,
  device_instance: 0,
  unique_number: 12345,
  model_id: "GPS 19x",
  software_version: "1.0",
  model_version: "1.0",
  model_serial: "",
  product_code: 100,
  first_seen: "2026-02-28T12:00:00Z",
  last_seen: "2026-02-28T12:00:01Z",
  packet_count: 100,
  byte_count: 800,
};

describe("parseSSE", () => {
  it("parses a frame event", async () => {
    const stream = makeStream(`data: ${JSON.stringify(sampleFrame)}\n\n`);
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("frame");
    if (events[0].type === "frame") {
      expect(events[0].frame.seq).toBe(42);
      expect(events[0].frame.pgn).toBe(129025);
      expect(events[0].frame.data).toBe("00aabbcc");
    }
  });

  it("parses a device event", async () => {
    const stream = makeStream(`data: ${JSON.stringify(sampleDevice)}\n\n`);
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("device");
    if (events[0].type === "device") {
      expect(events[0].device.manufacturer).toBe("Garmin");
      expect(events[0].device.src).toBe(1);
    }
  });

  it("handles multiple events in one stream", async () => {
    const text =
      `data: ${JSON.stringify(sampleFrame)}\n\n` +
      `data: ${JSON.stringify(sampleDevice)}\n\n` +
      `data: ${JSON.stringify({ ...sampleFrame, seq: 43 })}\n\n`;
    const stream = makeStream(text);
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(3);
    expect(events[0].type).toBe("frame");
    expect(events[1].type).toBe("device");
    expect(events[2].type).toBe("frame");
  });

  it("skips malformed JSON lines", async () => {
    const text =
      `data: not-json\n` +
      `data: ${JSON.stringify(sampleFrame)}\n\n`;
    const stream = makeStream(text);
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("frame");
  });

  it("skips non-data lines (comments, empty, event fields)", async () => {
    const text =
      `: this is a comment\n` +
      `event: message\n` +
      `id: 42\n` +
      `\n` +
      `data: ${JSON.stringify(sampleFrame)}\n\n`;
    const stream = makeStream(text);
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
  });

  it("handles data split across chunks", async () => {
    const full = `data: ${JSON.stringify(sampleFrame)}\n\n`;
    const mid = Math.floor(full.length / 2);
    const stream = makeChunkedStream(full.slice(0, mid), full.slice(mid));
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("frame");
  });

  it("handles empty stream", async () => {
    const stream = makeStream("");
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(0);
  });

  it("skips JSON that is not an object", async () => {
    const text =
      `data: 42\n` +
      `data: "hello"\n` +
      `data: [1,2,3]\n` +
      `data: null\n` +
      `data: ${JSON.stringify(sampleFrame)}\n\n`;
    const stream = makeStream(text);
    const events = [];
    for await (const event of parseSSE(stream)) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
  });
});
