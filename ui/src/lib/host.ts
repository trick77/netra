import type { Host } from "./api";
import { ABSENT } from "./format";

/**
 * The one definition of whether a host is reporting.
 *
 * It had grown three times over: the fleet column used 3x the scrape
 * interval, the fleet page's "N reporting" tile reimplemented the same rule
 * privately, and the host detail header used FIVE intervals with its own
 * vocabulary. Two of those can disagree on screen at the same moment -- a
 * tile saying 19 of 19 above a row marked offline, or a fleet list calling a
 * host down while its own detail page calls it reporting -- with nothing to
 * tell a user which is right.
 *
 * The threshold mirrors the product's own definition rather than inventing a
 * second one: the design spec's alerting rule is host-down = no POST within
 * 3x the scrape interval, and internal/agent/config/config.go fixes that
 * interval at 60s. When the alerting engine lands, the fleet list and the
 * engine must not disagree about which hosts are down.
 */
const SCRAPE_INTERVAL_S = 60;
// Exported because the host page's needsAttention() judges the same fact and
// used to carry its own five-minute constant. A host last seen four minutes
// ago then had its header say "offline", its traffic gauges blanked, its
// fleet row marked critical -- and the "Needs attention" panel directly below
// all of that say nothing needed attention. One definition of down, in the
// one place that states why it is three scrapes.
export const STALE_THRESHOLD_MS = 3 * SCRAPE_INTERVAL_S * 1000;

/**
 * How many recorded state changes in the last hour make a systemd unit
 * "restarting repeatedly".
 *
 * One definition for the whole UI: the host page's warning band decides
 * whether to warn about a unit, and the Units table decides whether to badge
 * its restart count, and a table that badges a unit the band calls fine is
 * worse than either answer on its own.
 *
 * Mirrors systemdstate.FlapThreshold in the hub, which is the copy that
 * decides whether the unit is returned by /units at all. No compiler can see
 * across that boundary -- change both.
 */
export const FLAP_THRESHOLD = 4;

export type HostStatus = {
  severity: "ok" | "warning" | "critical";
  /** The WORD inside the chip. Severity never rides on colour alone. */
  label: string;
};

/**
 * The share of a window's buckets a host may miss before it is reporting
 * badly rather than merely reporting.
 *
 * A host that answers now but dropped a fifth of the last few hours is not
 * healthy, and "online" is exactly as wrong a summary of it as "offline" --
 * both say the thing is fine or gone, when the interesting state is neither.
 */
const SPORADIC_MISS_RATIO = 0.2;

/**
 * Whether a series shows a host missing scrapes rather than reporting
 * cleanly.
 *
 * Both edges of the window are trimmed before anything is counted, for the
 * same reason: a null there is not a scrape the host failed to send.
 *
 * Trailing nulls are every tier materialising behind now (the 5m aggregate
 * by ten minutes), so the newest buckets are empty for every host on the
 * page, healthy or not. Counting those would mark the whole fleet sporadic.
 *
 * Leading nulls are the time before the host was reporting at all. The
 * window is always the range the PAGE is on -- the read API never clamps
 * `from` to when a host first appeared -- so a host added five minutes ago
 * gets 24h of grid with one real bucket at the end of it. Counting the
 * emptiness in front of it called every newly added agent sporadic on its
 * first day, and the badge only cleared once four fifths of the range had
 * elapsed since the host was added.
 *
 * The cost is that a host down for the first stretch of the window and
 * reporting cleanly ever since reads as online. That is the right answer for
 * a column whose wording is "gaps in the last few hours": one healed outage
 * at the far edge is not ongoing flakiness, and a host that is down NOW is
 * already critical by last_seen above. Telling "did not exist yet" apart
 * from "was down" needs a first_seen on the host, which the summary the
 * fleet reads does not carry.
 */
export function reportsSporadically(
  values: readonly (number | null)[],
): boolean {
  let end = values.length;
  while (end > 0 && values[end - 1] === null) end--;
  let start = 0;
  while (start < end && values[start] === null) start++;
  // Too little history to judge, measured over the span the host was
  // actually reporting across and not over the whole grid. Two buckets
  // cannot distinguish a gap from a host that started reporting mid-window.
  const span = end - start;
  if (span < 5) return false;
  let missed = 0;
  for (let i = start; i < end; i++) if (values[i] === null) missed++;
  // One gap is never "gaps in the last few hours". At the shortest span this
  // will judge, a single miss is exactly the 0.2 ratio, so a host added
  // twenty-five minutes ago that dropped one scrape while its agent settled
  // would be badged sporadic on the strength of that one bucket. Two is the
  // smallest number of misses that can be a pattern rather than an event.
  if (missed < 2) return false;
  return missed / span >= SPORADIC_MISS_RATIO;
}

export function hostStatus(
  host: Pick<Host, "last_seen">,
  now: Date = new Date(),
  /** A trend series for this host, if the caller has one. Given it, a host
   * that answers now but keeps dropping scrapes reports as sporadic rather
   * than as healthy -- the gaps are already drawn in its sparkline, and this
   * is the same fact said in a word. */
  trend?: readonly (number | null)[],
): HostStatus {
  if (host.last_seen === null) {
    // Never seen is not the same fact as gone quiet, and the host admin page
    // shows it as the expected state right after creation -- but for
    // anything watching the fleet, a host that has never reported is exactly
    // as absent as one that stopped.
    return { severity: "critical", label: "never seen" };
  }
  const ageMs = now.getTime() - new Date(host.last_seen).getTime();
  if (!Number.isFinite(ageMs) || ageMs > STALE_THRESHOLD_MS) {
    return { severity: "critical", label: "offline" };
  }
  if (trend !== undefined && reportsSporadically(trend)) {
    return { severity: "warning", label: "sporadic" };
  }
  return { severity: "ok", label: "online" };
}

/** True when the host is currently reporting — the tile's question.
 *
 * A sporadic host counts as reporting: it IS answering, just badly, and the
 * tile counts hosts the hub is hearing from. Its trouble is said in its own
 * row rather than by subtracting it from a headline count. */
export function isReporting(
  host: Pick<Host, "last_seen">,
  now: Date = new Date(),
): boolean {
  return hostStatus(host, now).severity !== "critical";
}

/**
 * The operating system, as a name rather than as an identifier.
 *
 * os_name is meant to carry the distribution -- every fixture in the repo
 * seeds it that way ("Ubuntu 24.04.1 LTS", "Debian GNU/Linux 12 (bookworm)")
 * -- but an agent that cannot read /etc/os-release falls back to Go's GOOS,
 * which is a build constant and always lowercase. Printing that raw put
 * "linux" on the page: true, but written as a compiler token rather than as
 * the name of an operating system.
 *
 * An explicit table, not capitalize(): capitalising GOOS gives "Darwin" for a
 * Mac and "Freebsd" for a BSD, and those are both wrong in a way that reads
 * as carelessness. A platform not in the table passes through untouched,
 * which is right for a distro string and harmless for anything else.
 *
 * Lives here rather than privately in the Overview tab that first needed it:
 * the host page's own header prints the same field two inches above the
 * System card, so a private copy in one of them is exactly how the header
 * came to read "linux" under a card reading "Linux".
 */
const OS_LABELS: Record<string, string> = {
  linux: "Linux",
  darwin: "macOS",
  windows: "Windows",
  freebsd: "FreeBSD",
  openbsd: "OpenBSD",
  netbsd: "NetBSD",
};

export function osLabel(name: string | null): string {
  if (name === null || name === "") return ABSENT;
  return OS_LABELS[name] ?? name;
}
