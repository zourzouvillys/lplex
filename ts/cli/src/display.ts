import type { Frame, Device } from "@sixfathoms/lplex";
import { pgnName } from "./pgn.js";
import { classLabel, funcLabel } from "./nmea.js";

// ANSI escape codes.
const RESET = "\x1b[0m";
const BOLD = "\x1b[1m";
const DIM = "\x1b[2m";
const GREEN = "\x1b[32m";
const YELLOW = "\x1b[33m";
const BLUE = "\x1b[34m";
const MAGENTA = "\x1b[35m";
const CYAN = "\x1b[36m";
const HI_GREEN = "\x1b[92m";
const HI_YELLOW = "\x1b[93m";
const HI_BLUE = "\x1b[94m";
const HI_MAGENTA = "\x1b[95m";
const HI_CYAN = "\x1b[96m";

const srcPalette = [
  GREEN,
  YELLOW,
  BLUE,
  MAGENTA,
  CYAN,
  HI_GREEN,
  HI_YELLOW,
  HI_BLUE,
  HI_MAGENTA,
  HI_CYAN,
];

function colorForSrc(src: number): string {
  return srcPalette[src % srcPalette.length];
}

export type DeviceMap = Map<number, Device>;

function formatLocalTime(ts: string): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  const ms = String(d.getMilliseconds()).padStart(3, "0");
  return `${hh}:${mm}:${ss}.${ms}`;
}

function formatTimeShort(ts: string): string {
  if (!ts || ts === "0001-01-01T00:00:00Z") return "";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "";
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  const ss = String(d.getSeconds()).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

export function formatBytes(b: number): string {
  if (b >= 1 << 30) return `${(b / (1 << 30)).toFixed(1)} GB`;
  if (b >= 1 << 20) return `${(b / (1 << 20)).toFixed(1)} MB`;
  if (b >= 1 << 10) return `${(b / (1 << 10)).toFixed(1)} KB`;
  return `${b} B`;
}

function displayManufacturer(d: Device): string {
  if (d.manufacturer) return `${d.manufacturer} (${d.manufacturer_code})`;
  return `[src=${d.src}]`;
}

export function formatFrame(f: Frame, devices: DeviceMap): string {
  const ts = formatLocalTime(f.ts);

  let srcLabel = `[${f.src}]`;
  const dev = devices.get(f.src);
  if (dev?.manufacturer) {
    srcLabel = `${dev.manufacturer}(${dev.manufacturer_code})[${f.src}]`;
  }

  const name = pgnName(f.pgn);
  const sc = colorForSrc(f.src);
  const dst = f.dst.toString(16).padStart(2, "0");

  let line =
    `${DIM}${ts}${RESET} ` +
    `${DIM}#${String(f.seq).padEnd(7)}${RESET} ` +
    `${sc}${BOLD}${srcLabel.padEnd(20)}${RESET} ` +
    `${DIM}>${dst}${RESET}  ` +
    `${CYAN}${BOLD}${String(f.pgn).padEnd(6)}${RESET}`;

  if (name) {
    line += ` ${CYAN}${name.padEnd(22)}${RESET}`;
  } else {
    line += " ".repeat(23);
  }

  line += ` ${DIM}p${f.prio}${RESET}  ${f.data}`;
  return line;
}

export function printDeviceTable(devices: Device[]): string {
  if (devices.length === 0) return "";

  const sorted = [...devices].sort((a, b) => a.src - b.src);

  interface Row {
    dev: Device;
    mfctr: string;
    model: string;
    cls: string;
    fn: string;
    traffic: string;
    first: string;
    last: string;
  }

  let mfctrW = "MANUFACTURER".length;
  let modelW = "MODEL".length;
  let classW = "CLASS".length;
  let funcW = "FUNCTION".length;
  let trafficW = "TRAFFIC".length;

  const rows: Row[] = sorted.map((d) => {
    const mfctr = displayManufacturer(d);
    let model = d.model_id || "";
    if (d.product_code > 0) model = `${model} (${d.product_code})`;
    const traffic = formatBytes(d.byte_count || 0);
    const cls = classLabel(d.device_class);
    const fn = funcLabel(d.device_class, d.device_function);

    mfctrW = Math.max(mfctrW, mfctr.length);
    modelW = Math.max(modelW, model.length);
    classW = Math.max(classW, cls.length);
    funcW = Math.max(funcW, fn.length);
    trafficW = Math.max(trafficW, traffic.length);

    return {
      dev: d,
      mfctr,
      model,
      cls,
      fn,
      traffic,
      first: formatTimeShort(d.first_seen),
      last: formatTimeShort(d.last_seen),
    };
  });

  const hLine = (left: string, mid: string, right: string, fill: string) =>
    left +
    fill.repeat(5) +
    mid +
    fill.repeat(18) +
    mid +
    fill.repeat(mfctrW + 2) +
    mid +
    fill.repeat(modelW + 2) +
    mid +
    fill.repeat(classW + 2) +
    mid +
    fill.repeat(funcW + 2) +
    mid +
    fill.repeat(6) +
    mid +
    fill.repeat(trafficW + 2) +
    mid +
    fill.repeat(10) +
    mid +
    fill.repeat(10) +
    right;

  const top = hLine("\u250c", "\u252c", "\u2510", "\u2500");
  const sep = hLine("\u251c", "\u253c", "\u2524", "\u2500");
  const bot = hLine("\u2514", "\u2534", "\u2518", "\u2500");

  const lines: string[] = [];
  lines.push("");
  lines.push(`${DIM}${top}${RESET}`);

  // Header row.
  lines.push(
    `${DIM}\u2502${RESET} ${BOLD}SRC${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}${"NAME".padEnd(16)}${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}${"MANUFACTURER".padEnd(mfctrW)}${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}${"MODEL".padEnd(modelW)}${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}${"CLASS".padEnd(classW)}${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}${"FUNCTION".padEnd(funcW)}${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}INST${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}${"TRAFFIC".padStart(trafficW)}${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}FIRST   ${RESET} ` +
      `${DIM}\u2502${RESET} ${BOLD}LAST    ${RESET} ` +
      `${DIM}\u2502${RESET}`,
  );

  lines.push(`${DIM}${sep}${RESET}`);

  for (const r of rows) {
    const sc = colorForSrc(r.dev.src);
    lines.push(
      `${DIM}\u2502${RESET} ${sc}${BOLD}${String(r.dev.src).padStart(3)}${RESET} ` +
        `${DIM}\u2502${RESET} ${DIM}${r.dev.name.padEnd(16)}${RESET} ` +
        `${DIM}\u2502${RESET} ${sc}${r.mfctr.padEnd(mfctrW)}${RESET} ` +
        `${DIM}\u2502${RESET} ${r.model.padEnd(modelW)} ` +
        `${DIM}\u2502${RESET} ${r.cls.padEnd(classW)} ` +
        `${DIM}\u2502${RESET} ${r.fn.padEnd(funcW)} ` +
        `${DIM}\u2502${RESET} ${String(r.dev.device_instance).padStart(4)} ` +
        `${DIM}\u2502${RESET} ${r.traffic.padStart(trafficW)} ` +
        `${DIM}\u2502${RESET} ${r.first.padEnd(8)} ` +
        `${DIM}\u2502${RESET} ${r.last.padEnd(8)} ` +
        `${DIM}\u2502${RESET}`,
    );
  }

  lines.push(`${DIM}${bot}${RESET}`);
  lines.push("");

  return lines.join("\n");
}
