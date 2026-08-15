// Decimal (1000-based) units throughout, matching how disks, links and
// vendors advertise capacity -- binary (1024-based) units would silently
// disagree with every spec sheet the numbers get compared against.
//
// Installed memory is the one exception, and it is the same argument rather
// than a break from it: RAM is the one component whose spec sheet is written
// in binary units. A 32 GiB machine reports ~33.3e9 bytes of MemTotal, and
// rendering that decimally says "33.3 GB" beside a box everyone calls a 32 GB
// box. See binaryBytes.

// Exported so every call site renders the same absent marker instead of
// each inventing its own dash (or worse, a hardcoded "-" that silently
// stops matching this one when this changes).
export const ABSENT = "—";

function round(n: number, digits: number): number {
  const f = 10 ** digits;
  return Math.round(n * f) / f;
}

// Scales `n` into the first unit whose threshold it clears, formatting with
// zero decimals at the base unit and one decimal above it -- "503 GB" reads
// as a measurement, "503.0 GB" reads as false precision. Rounding can push
// a value like 999.96 GB up to "1000 GB"; when that happens, promote to the
// next unit so the display never shows a rounded value at or above the base.
// The guard is written against `base` rather than a literal 1000 for that
// reason: with base 1024 the boundary is 1024, and a hardcoded 1000 would
// promote 1000-1023 GiB a unit early and print "1 TiB" for 1000 GiB.
function scale(n: number, units: string[], base = 1000): string {
  const abs = Math.abs(n);
  let idx = 0;
  for (let i = units.length - 1; i >= 0; i--) {
    if (abs >= base ** i) {
      idx = i;
      break;
    }
  }
  let digits = idx === 0 ? 0 : 1;
  let rounded = round(n / base ** idx, digits);
  if (Math.abs(rounded) >= base && idx < units.length - 1) {
    idx++;
    digits = idx === 0 ? 0 : 1;
    rounded = round(n / base ** idx, digits);
  }
  return `${rounded} ${units[idx]}`;
}

/** Storage and traffic, in bytes. Decimal: 1 kB = 1000 B. */
export function bytes(n: number | null): string {
  if (n === null) return ABSENT;
  return scale(n, ["B", "kB", "MB", "GB", "TB", "PB"]);
}

/**
 * Installed memory, in bytes. Binary: 1 KiB = 1024 B.
 *
 * Only for RAM, and only because RAM is bought in binary units. /proc/meminfo
 * reports MemTotal in KiB, the agent multiplies it up to real bytes, and the
 * operator compares the result against the "32 GB" on the invoice -- which is
 * 32 GiB. Rendered decimally the same byte count reads 33.3 GB: not wrong,
 * but ~7% above every figure it will be checked against, and above the
 * machine's own capacity besides.
 *
 * Disks and links keep bytes(): those really are sold decimally, and this
 * would disagree with their spec sheets in the opposite direction.
 */
export function binaryBytes(n: number | null): string {
  if (n === null) return ABSENT;
  return scale(n, ["B", "KiB", "MiB", "GiB", "TiB", "PiB"], 1024);
}

/**
 * A plain count of things -- open descriptors, tracked connections, sockets
 * -- grouped in threes.
 *
 * Deliberately NOT scale(): the exhaustion meters read "48 231 of 262 144",
 * and rounding those to "48 k of 262 k" throws away the only digits that
 * distinguish comfortable from nearly-full. These are cardinal counts
 * against a kernel limit, not magnitudes on a chart axis.
 *
 * A narrow no-break space groups the digits, which is unambiguous in every
 * locale -- a comma is a decimal separator to half of Europe, including
 * where this is being read.
 *
 * Named cardinal rather than count because Graphs.tsx already has a local
 * count(): a chart-tick formatter that rounds to two decimals. Two
 * functions of that name with different rules is how a caller reaches for
 * the wrong one, so the second spelling says what it is instead.
 */
export function cardinal(n: number | null): string {
  if (n === null) return ABSENT;
  const sign = n < 0 ? "-" : "";
  const digits = Math.round(Math.abs(n)).toString();
  let out = "";
  for (let i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 === 0) out += "\u202f";
    out += digits[i];
  }
  return sign + out;
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

/**
 * Network and disk throughput, in BYTES per second -- the counterpart to
 * `bitrate` and, for netra, the far commoner of the two.
 *
 * Every rate the agent reports is bytes: internal/agent/collector/network.go
 * computes net_rx/net_tx as (rxBytes - prev) / elapsed, and the container
 * collector does the same for its own counters. Handing one of those to
 * `bitrate` is precisely the 8x-wrong-but-plausible number that function's
 * doc comment warns about, and both traffic call sites did it -- the fleet
 * row's cell and the host overview's Traffic card, which is why one page
 * agreeing with another proved nothing.
 *
 * It lives here rather than beside a call site so there is exactly one
 * spelling of "bytes per second" in the app. A local copy was how the two
 * traffic cells stayed wrong while the container page was right.
 */
export function byterate(bytesPerSecond: number | null): string {
  if (bytesPerSecond === null) return ABSENT;
  return `${bytes(bytesPerSecond)}/s`;
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
 *
 * Accepts `null` -- every numeric formatter above absorbs `null` into
 * `ABSENT`, and a date field on the wire is just as often absent
 * (`Host.last_seen: string | null`); rejecting `null` here would force
 * every call site to guard it separately and invent its own dash. An
 * unparseable string (`new Date(iso)` yielding `Invalid Date`) is treated
 * the same way rather than rendered as `"NaN d ago"`, which is not a
 * unit-bearing quantity and communicates nothing to the reader.
 */
export function relative(iso: string | null, now: Date = new Date()): string {
  if (iso === null) return ABSENT;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return ABSENT;
  const seconds = Math.max(0, Math.round((now.getTime() - then) / 1000));
  return `${duration(seconds)} ago`;
}

/**
 * Fixed-instant rendering for hover titles, where the reader wants the
 * exact wall-clock time rather than an age that keeps ticking.
 *
 * Accepts `null` and an unparseable string for the same reason `relative`
 * does -- see its doc comment.
 */
export function absolute(iso: string | null, tz?: string): string {
  if (iso === null) return ABSENT;
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return ABSENT;
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

/**
 * Age of an epoch-millisecond instant relative to `now`, for the one
 * timestamp shape in the read API that is NOT an ISO string: metrics.ts's
 * `seriesTimestamps()` returns epoch millis, transcribing
 * internal/hub/read/metrics.go:198's `ts.UnixMilli()` verbatim. Kept as a
 * separate function from `relative` rather than widening `relative` to
 * accept `string | number`: a single signature accepting both shapes
 * invites a caller passing an epoch-millis number where an ISO string was
 * expected (`new Date(1700000000000 as unknown as string)` parses to
 * `Invalid Date` silently) with no type error to catch the mistake.
 */
export function relativeMs(ms: number | null, now: Date = new Date()): string {
  if (ms === null || !Number.isFinite(ms)) return ABSENT;
  const seconds = Math.max(0, Math.round((now.getTime() - ms) / 1000));
  return `${duration(seconds)} ago`;
}

/** Fixed-instant counterpart to `relativeMs`, for the same epoch-millis x-axis values. */
export function absoluteMs(ms: number | null, tz?: string): string {
  if (ms === null || !Number.isFinite(ms)) return ABSENT;
  const date = new Date(ms);
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
