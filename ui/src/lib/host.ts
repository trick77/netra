import type { Host } from "./api";

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
const STALE_THRESHOLD_MS = 3 * SCRAPE_INTERVAL_S * 1000;

export type HostStatus = {
  severity: "ok" | "warning" | "critical";
  /** The WORD beside the dot. Severity never rides on colour alone. */
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
