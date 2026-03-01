import { describe, it, expect } from "vitest";
import { Client } from "../src/client.js";
import { HttpError } from "../src/errors.js";

const sampleFrame = {
  seq: 1,
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

function sseResponse(lines: string): Response {
  const encoder = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(lines));
      controller.close();
    },
  });
  return new Response(body, {
    status: 200,
    headers: { "Content-Type": "text/event-stream" },
  });
}

function jsonResponse(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function errorResponse(status: number, body: string): Response {
  return new Response(body, { status });
}

describe("Client.devices", () => {
  it("fetches and returns the device list", async () => {
    const devices = [{ ...sampleDevice }];
    const mockFetch = async (url: string | URL | Request) => {
      expect(url).toBe("http://localhost:8089/devices");
      return jsonResponse(devices);
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    const result = await client.devices();
    expect(result).toHaveLength(1);
    expect(result[0].manufacturer).toBe("Garmin");
  });

  it("throws HttpError on non-200", async () => {
    const mockFetch = async () => errorResponse(500, "internal error");
    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    await expect(client.devices()).rejects.toThrow(HttpError);
  });
});

describe("Client.subscribe", () => {
  it("opens an ephemeral SSE stream", async () => {
    const mockFetch = async (url: string | URL | Request) => {
      expect(String(url)).toBe("http://localhost:8089/events");
      return sseResponse(`data: ${JSON.stringify(sampleFrame)}\n\n`);
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    const stream = await client.subscribe();
    const events = [];
    for await (const event of stream) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("frame");
  });

  it("encodes filter as query params", async () => {
    let capturedURL = "";
    const mockFetch = async (url: string | URL | Request) => {
      capturedURL = String(url);
      return sseResponse("");
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    await client.subscribe({ pgn: [129025, 129026], manufacturer: ["Garmin"] });

    const parsed = new URL(capturedURL);
    expect(parsed.searchParams.getAll("pgn")).toEqual(["129025", "129026"]);
    expect(parsed.searchParams.getAll("manufacturer")).toEqual(["Garmin"]);
  });

  it("omits query string when filter is empty", async () => {
    let capturedURL = "";
    const mockFetch = async (url: string | URL | Request) => {
      capturedURL = String(url);
      return sseResponse("");
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    await client.subscribe({});
    expect(capturedURL).toBe("http://localhost:8089/events");
  });
});

describe("Client.send", () => {
  it("sends a CAN frame", async () => {
    let capturedBody = "";
    const mockFetch = async (_url: string | URL | Request, init?: RequestInit) => {
      capturedBody = init?.body as string;
      return new Response(null, { status: 202 });
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    await client.send({ pgn: 129025, src: 0, dst: 255, prio: 6, data: "aabb" });
    const parsed = JSON.parse(capturedBody);
    expect(parsed.pgn).toBe(129025);
    expect(parsed.data).toBe("aabb");
  });

  it("throws on non-202", async () => {
    const mockFetch = async () => errorResponse(503, "tx queue full");
    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    await expect(
      client.send({ pgn: 129025, src: 0, dst: 255, prio: 6, data: "aa" }),
    ).rejects.toThrow(HttpError);
  });
});

describe("Client.createSession", () => {
  it("creates a session and returns a Session object", async () => {
    const sessionInfo = {
      client_id: "my-client",
      seq: 100,
      cursor: 0,
      devices: [],
    };

    let capturedBody = "";
    const mockFetch = async (url: string | URL | Request, init?: RequestInit) => {
      const u = String(url);
      if (u.endsWith("/clients/my-client") && init?.method === "PUT") {
        capturedBody = init.body as string;
        return jsonResponse(sessionInfo);
      }
      return errorResponse(404, "not found");
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    const session = await client.createSession({
      clientId: "my-client",
      bufferTimeout: "PT5M",
      filter: { pgn: [129025] },
    });

    expect(session.info.client_id).toBe("my-client");
    expect(session.info.seq).toBe(100);
    expect(session.lastAckedSeq).toBe(0);

    const body = JSON.parse(capturedBody);
    expect(body.buffer_timeout).toBe("PT5M");
    expect(body.filter.pgn).toEqual([129025]);
  });
});

describe("Session", () => {
  it("subscribes and receives events", async () => {
    const sessionInfo = {
      client_id: "test-session",
      seq: 50,
      cursor: 0,
      devices: [],
    };

    const mockFetch = async (url: string | URL | Request, init?: RequestInit) => {
      const u = String(url);
      if (u.endsWith("/clients/test-session") && init?.method === "PUT") {
        return jsonResponse(sessionInfo);
      }
      if (u.endsWith("/clients/test-session/events")) {
        return sseResponse(`data: ${JSON.stringify(sampleFrame)}\n\n`);
      }
      return errorResponse(404, "not found");
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    const session = await client.createSession({
      clientId: "test-session",
      bufferTimeout: "PT1M",
    });

    const stream = await session.subscribe();
    const events = [];
    for await (const event of stream) {
      events.push(event);
    }
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("frame");
  });

  it("acks a sequence number", async () => {
    const sessionInfo = {
      client_id: "ack-test",
      seq: 50,
      cursor: 0,
      devices: [],
    };

    let ackSeq = -1;
    const mockFetch = async (url: string | URL | Request, init?: RequestInit) => {
      const u = String(url);
      if (u.endsWith("/clients/ack-test") && init?.method === "PUT") {
        return jsonResponse(sessionInfo);
      }
      if (u.endsWith("/clients/ack-test/ack") && init?.method === "PUT") {
        ackSeq = JSON.parse(init.body as string).seq;
        return new Response(null, { status: 204 });
      }
      return errorResponse(404, "not found");
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    const session = await client.createSession({
      clientId: "ack-test",
      bufferTimeout: "PT1M",
    });

    await session.ack(42);
    expect(ackSeq).toBe(42);
    expect(session.lastAckedSeq).toBe(42);
  });

  it("throws HttpError when ack fails", async () => {
    const sessionInfo = {
      client_id: "fail-ack",
      seq: 50,
      cursor: 0,
      devices: [],
    };

    const mockFetch = async (url: string | URL | Request, init?: RequestInit) => {
      const u = String(url);
      if (u.endsWith("/clients/fail-ack") && init?.method === "PUT") {
        if (u.endsWith("/ack")) {
          return errorResponse(404, "session not found");
        }
        return jsonResponse(sessionInfo);
      }
      return errorResponse(404, "not found");
    };

    const client = new Client("http://localhost:8089", {
      fetch: mockFetch as typeof fetch,
    });
    const session = await client.createSession({
      clientId: "fail-ack",
      bufferTimeout: "PT1M",
    });

    await expect(session.ack(42)).rejects.toThrow(HttpError);
  });
});
