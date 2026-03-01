export class LplexError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "LplexError";
  }
}

export class HttpError extends LplexError {
  readonly status: number;
  readonly body: string;

  constructor(method: string, path: string, status: number, body: string) {
    super(`${method} ${path} returned ${status}: ${body}`);
    this.name = "HttpError";
    this.status = status;
    this.body = body;
  }
}
