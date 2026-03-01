import type { Event, SessionInfo } from "./types.js";
import { HttpError } from "./errors.js";
import { parseSSE } from "./sse.js";

type FetchFn = typeof globalThis.fetch;

export class Session {
  readonly #baseURL: string;
  readonly #fetch: FetchFn;
  readonly #info: SessionInfo;
  #lastAckedSeq: number = 0;

  /** @internal Created by Client.createSession, not for direct use. */
  constructor(baseURL: string, fetchFn: FetchFn, info: SessionInfo) {
    this.#baseURL = baseURL;
    this.#fetch = fetchFn;
    this.#info = info;
  }

  get info(): SessionInfo {
    return this.#info;
  }

  get lastAckedSeq(): number {
    return this.#lastAckedSeq;
  }

  /**
   * Open the SSE stream for this session. Replays buffered frames
   * from the cursor, then streams live.
   */
  async subscribe(signal?: AbortSignal): Promise<AsyncIterable<Event>> {
    const url = `${this.#baseURL}/clients/${this.#info.client_id}/events`;
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

  /** Advance the cursor for this session to the given sequence number. */
  async ack(seq: number, signal?: AbortSignal): Promise<void> {
    const url = `${this.#baseURL}/clients/${this.#info.client_id}/ack`;
    const resp = await this.#fetch(url, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ seq }),
      signal,
    });

    if (resp.status !== 204) {
      const body = await resp.text();
      throw new HttpError("PUT", url, resp.status, body);
    }

    this.#lastAckedSeq = seq;
  }
}
