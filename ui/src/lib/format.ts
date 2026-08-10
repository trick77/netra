// Decimal (1000-based) units throughout, matching how disks, links and
// vendors advertise capacity -- binary (1024-based) units would silently
// disagree with every spec sheet the numbers get compared against.

const ABSENT = "—";

function round(n: number, digits: number): number {
  const f = 10 ** digits;
  return Math.round(n * f) / f;
}

// Scales `n` into the first unit whose threshold it clears, formatting with
// zero decimals at the base unit and one decimal above it -- "503 GB" reads
// as a measurement, "503.0 GB" reads as false precision. Rounding can push
// a value like 999.96 GB up to "1000 GB"; when that happens, promote to the
// next unit so the display never shows a rounded value at or above 1000.
function scale(n: number, units: string[]): string {
  const abs = Math.abs(n);
  let idx = 0;
  for (let i = units.length - 1; i >= 0; i--) {
    if (abs >= 1000 ** i) {
      idx = i;
      break;
    }
  }
  let digits = idx === 0 ? 0 : 1;
  let rounded = round(n / 1000 ** idx, digits);
  if (Math.abs(rounded) >= 1000 && idx < units.length - 1) {
    idx++;
    digits = idx === 0 ? 0 : 1;
    rounded = round(n / 1000 ** idx, digits);
  }
  return `${rounded} ${units[idx]}`;
}

/** Storage and memory, in bytes. Decimal: 1 kB = 1000 B. */
export function bytes(n: number | null): string {
  if (n === null) return ABSENT;
  return scale(n, ["B", "kB", "MB", "GB", "TB", "PB"]);
}

/**
 * Network throughput, in bits per second. Kept as a distinct function from
 * `bytes` on purpose -- storage is bytes, network is bits, and collapsing
 * the two into one "size" formatter is the classic silent monitoring-UI
 * bug: an 8x-wrong number that still looks plausible on a dashboard.
 */
export function bitrate(bitsPerSecond: number | null): string {
  if (bitsPerSecond === null) return ABSENT;
  return scale(bitsPerSecond, ["b/s", "kb/s", "Mb/s", "Gb/s", "Tb/s"]);
}

export function percent(n: number | null, digits = 0): string {
  if (n === null) return ABSENT;
  return `${round(n, digits)} %`;
}

const DURATION_UNITS: Array<[string, number]> = [
  ["d", 86400],
  ["h", 3600],
  ["m", 60],
  ["s", 1],
];

/**
 * Renders at most two units of precision ("266 d 6 h", not
 * "266 d 6 h 41 m") -- past the top two units the smaller ones are noise
 * for a human scanning a status page.
 */
export function duration(seconds: number | null): string {
  if (seconds === null) return ABSENT;
  let remaining = Math.max(0, Math.round(seconds));
  const parts: string[] = [];
  for (const [label, size] of DURATION_UNITS) {
    if (parts.length >= 2) break;
    const count = Math.floor(remaining / size);
    if (count === 0 && parts.length === 0 && size !== 1) continue;
    // A second unit that rounds to zero (e.g. "4 m 0 s") is noise, not
    // precision -- drop it rather than pad the two-unit budget with a zero.
    if (count === 0 && parts.length === 1) break;
    parts.push(`${count} ${label}`);
    remaining -= count * size;
  }
  return parts.length > 0 ? parts.join(" ") : "0 s";
}

/**
 * Age of `iso` relative to `now`, at the same two-unit precision as
 * `duration`. `now` is injectable so tests are deterministic instead of
 * racing the system clock.
 */
export function relative(iso: string, now: Date = new Date()): string {
  const then = new Date(iso).getTime();
  const seconds = Math.max(0, Math.round((now.getTime() - then) / 1000));
  return `${duration(seconds)} ago`;
}

/**
 * Fixed-instant rendering for hover titles, where the reader wants the
 * exact wall-clock time rather than an age that keeps ticking.
 */
export function absolute(iso: string, tz?: string): string {
  const date = new Date(iso);
  return date.toLocaleString("en-GB", {
    timeZone: tz,
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}
