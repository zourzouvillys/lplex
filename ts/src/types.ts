/** A single CAN frame received from the lplex server. */
export interface Frame {
  seq: number;
  ts: string;
  prio: number;
  pgn: number;
  src: number;
  dst: number;
  data: string;
}

/** An NMEA 2000 device discovered on the bus. */
export interface Device {
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

/** Discriminated union for SSE events. */
export type Event =
  | { type: "frame"; frame: Frame }
  | { type: "device"; device: Device };

/**
 * Filter for CAN frames.
 * Categories are AND'd, values within a category are OR'd.
 */
export interface Filter {
  pgn?: number[];
  manufacturer?: string[];
  instance?: number[];
  name?: string[];
}

/** Configuration for creating a buffered session. */
export interface SessionConfig {
  clientId: string;
  bufferTimeout: string;
  filter?: Filter;
}

/** Server response from creating or reconnecting a session. */
export interface SessionInfo {
  client_id: string;
  seq: number;
  cursor: number;
  devices: Device[];
}

/** Parameters for transmitting a CAN frame. */
export interface SendParams {
  pgn: number;
  src: number;
  dst: number;
  prio: number;
  data: string;
}
