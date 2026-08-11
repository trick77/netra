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
  severity: "ok" | "critical";
  /** The WORD beside the dot. Severity never rides on colour alone. */
  label: string;
};

export function hostStatus(
  host: Pick<Host, "last_seen">,
  now: Date = new Date(),
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
  return { severity: "ok", label: "online" };
}

/** True when the host is currently reporting — the tile's question. */
export function isReporting(
  host: Pick<Host, "last_seen">,
  now: Date = new Date(),
): boolean {
  return hostStatus(host, now).severity === "ok";
}
