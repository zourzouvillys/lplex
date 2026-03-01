import type {
  Device,
  Event,
  Filter,
  SendParams,
  SessionConfig,
  SessionInfo,
} from "./types.js";
import { HttpError } from "./errors.js";
import { parseSSE } from "./sse.js";
import { Session } from "./session.js";

type FetchFn = typeof globalThis.fetch;

export interface ClientOptions {
  fetch?: FetchFn;
}

export class Client {
  readonly #baseURL: string;
  readonly #fetch: FetchFn;

  constructor(baseURL: string, options?: ClientOptions) {
    this.#baseURL = baseURL.replace(/\/+$/, "");
    this.#fetch = options?.fetch ?? globalThis.fetch.bind(globalThis);
  }

  /** Fetch a snapshot of all NMEA 2000 devices discovered by the server. */
  async devices(signal?: AbortSignal): Promise<Device[]> {
    const url = `${this.#baseURL}/devices`;
    const resp = await this.#fetch(url, { signal });

    if (!resp.ok) {
      const body = await resp.text();
      throw new HttpError("GET", url, resp.status, body);
    }

    return resp.json() as Promise<Device[]>;
  }

  /**
   * Open an ephemeral SSE stream with optional filtering.
   * No session, no replay, no ACK.
   */
  async subscribe(
    filter?: Filter,
    signal?: AbortSignal,
  ): Promise<AsyncIterable<Event>> {
    let url = `${this.#baseURL}/events`;
    const qs = filterToQueryString(filter);
    if (qs) url += `?${qs}`;

    const resp = await this.#fetch(url, {
      headers: { Accept: "text/event-stream" },
      signal,
    });

    if (!resp.ok) {
      const body = await resp.text();
      throw new HttpError("GET", url, resp.status, body);
    }

    if (!resp.body) {
      throw new HttpError("GET", url, resp.status, "no response body");
    }

    return parseSSE(resp.body);
  }

  /** Transmit a CAN frame through the server. */
  async send(params: SendParams, signal?: AbortSignal): Promise<void> {
    const url = `${this.#baseURL}/send`;
    const resp = await this.#fetch(url, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(params),
      signal,
    });

    if (resp.status !== 202) {
      const body = await resp.text();
      throw new HttpError("POST", url, resp.status, body);
    }
  }

  /** Create or reconnect a buffered session on the server. */
  async createSession(
    config: SessionConfig,
    signal?: AbortSignal,
  ): Promise<Session> {
    const url = `${this.#baseURL}/clients/${config.clientId}`;

    const putBody: Record<string, unknown> = {
      buffer_timeout: config.bufferTimeout,
    };
    if (config.filter && !filterIsEmpty(config.filter)) {
      putBody.filter = filterToJSON(config.filter);
    }

    const resp = await this.#fetch(url, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(putBody),
      signal,
    });

    if (!resp.ok) {
      const body = await resp.text();
      throw new HttpError("PUT", url, resp.status, body);
    }

    const info = (await resp.json()) as SessionInfo;
    return new Session(this.#baseURL, this.#fetch, info);
  }
}

function filterIsEmpty(f: Filter): boolean {
  return (
    !f.pgn?.length &&
    !f.manufacturer?.length &&
    !f.instance?.length &&
    !f.name?.length
  );
}

function filterToQueryString(f?: Filter): string {
  if (!f || filterIsEmpty(f)) return "";

  const params = new URLSearchParams();
  f.pgn?.forEach((p) => params.append("pgn", p.toString()));
  f.manufacturer?.forEach((m) => params.append("manufacturer", m));
  f.instance?.forEach((i) => params.append("instance", i.toString()));
  f.name?.forEach((n) => params.append("name", n));
  return params.toString();
}

function filterToJSON(f: Filter): Record<string, unknown> {
  const m: Record<string, unknown> = {};
  if (f.pgn?.length) m.pgn = f.pgn;
  if (f.manufacturer?.length) m.manufacturer = f.manufacturer;
  if (f.instance?.length) m.instance = f.instance;
  if (f.name?.length) m.name = f.name;
  return m;
}
