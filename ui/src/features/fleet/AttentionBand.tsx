import type { ReactNode } from "react";
import { Badge, type Severity } from "../../ui/Badge";
import { relative } from "../../lib/format";
import type { HostTab } from "../host/HostPage";

/**
 * A host-level condition worth surfacing on the overview. `what` is a
 * ReactNode (not string) so a caller can embed a value inline (e.g. "disk
 * 92% full") without this component reaching back into formatting logic
 * it has no business owning.
 */
export interface Condition {
  hostId: string;
  hostname: string;
  severity: Severity;
  what: ReactNode;
  /**
   * When this started, when that is genuinely known -- and null when it is
   * not.
   *
   * Most conditions have no honest onset. A filesystem at 91% crossed 90 at
   * some point netra never recorded; an OOM kill happened inside the window
   * but the counter says only that the total moved. The obvious stand-ins --
   * the window start, last_seen, now -- are all a timestamp the reader would
   * take literally, and "since 5 m ago" beside a disk that has been filling
   * for a week is worse than saying nothing. Rows with no onset simply do
   * not carry the column.
   */
  since: string | null;
  /**
   * The host tab that answers this condition in full, when one does.
   *
   * Every row used to end at the sentence, and for half of them that was a
   * dead end: "1 failed unit" and "/mnt/ark is 93% full" both have a page
   * that lists exactly what the band is summarising, and the reader had to
   * know it existed and navigate there by hand. null is for the conditions
   * with no such page -- a host that stopped reporting is not explained
   * better by any one tab, and a link that lands somewhere unhelpful teaches
   * people to stop following links.
   */
  tab: HostTab | null;
}

export interface HostGroup {
  hostId: string;
  hostname: string;
  /** Every condition for this host, worst first -- grouping is presentation,
   * never suppression, so nothing is dropped here. */
  conditions: Condition[];
  /** The single worst condition, used as this group's sort key. */
  worst: Condition;
}

// Higher rank == worse. `ok` and `neutral` never appear in practice (an
// attention band condition is definitionally something wrong), but are
// ranked lowest so a stray one sorts to the bottom rather than crashing.
const SEVERITY_RANK: Record<Severity, number> = {
  critical: 4,
  serious: 3,
  warning: 2,
  ok: 1,
  neutral: 0,
};

function worstOf(conditions: Condition[]): Condition {
  return conditions.reduce((worst, c) =>
    SEVERITY_RANK[c.severity] > SEVERITY_RANK[worst.severity] ? c : worst,
  );
}

/**
 * Groups conditions by host and orders the groups by each host's WORST
 * condition, not by how many conditions it has -- a host with one
 * critical outranks a host with four warnings, so a noisy-but-healthy
 * host never displaces a genuinely broken one. Exported separately so
 * this ordering rule is unit-testable without rendering anything.
 *
 * Within a host the conditions are sorted worst-first too, which they did not
 * used to be: the band promoted a host's worst condition into the host's own
 * row and left the rest in the order hostConditions() wrote them. Now that
 * every condition is an equal row, that promotion is gone -- and with it the
 * guarantee that came free with it, that a host's worst was always the one on
 * screen. Sorting here restores it, and it matters at the fold: a host with
 * four conditions shows three, and the one it hides must not be the critical.
 *
 * The sort is stable, so hostConditions()'s own ordering survives inside each
 * severity -- reporting still leads the criticals, which is the whole reason
 * it is written first.
 */
export function groupByHost(conditions: Condition[]): HostGroup[] {
  const byHost = new Map<string, Condition[]>();
  for (const c of conditions) {
    const existing = byHost.get(c.hostId);
    if (existing) {
      existing.push(c);
    } else {
      byHost.set(c.hostId, [c]);
    }
  }
  const groups: HostGroup[] = Array.from(byHost.entries()).map(
    ([hostId, hostConditions]) => ({
      hostId,
      hostname: hostConditions[0].hostname,
      conditions: [...hostConditions].sort(
        (a, b) => SEVERITY_RANK[b.severity] - SEVERITY_RANK[a.severity],
      ),
      worst: worstOf(hostConditions),
    }),
  );
  groups.sort(
    (a, b) => SEVERITY_RANK[b.worst.severity] - SEVERITY_RANK[a.worst.severity],
  );
  return groups;
}

// Past this many conditions on a single host, a cascading failure (disk
// fills -> services fail -> load climbs -> containers restart) would
// otherwise flood the band and bury a second host quietly starting to
// fail. Three rows read fine under one hostname, so that is the threshold --
// it was 2 while the host's own row carried the worst condition and the
// disclosed rows were the leftovers.
const MAX_CONDITION_ROWS = 3;

// If the band itself grows past this many host rows, cap it and say so --
// silent truncation reads as completeness, which is worse than a long band.
const MAX_HOST_ROWS = 20;

export interface AttentionBandProps {
  conditions: Condition[];
  /** Optional per-row action slot (e.g. a later "Explain" affordance).
   * This component only reserves the space -- it does not build the
   * affordance itself. */
  renderAction?: (group: HostGroup) => ReactNode;
}

/**
 * What is wrong with the fleet, grouped by host.
 *
 * The band used to carry its own header, "{n} on {m} hosts". It said the same
 * thing as the line the page prints directly above it ("3 of 3 hosts need
 * attention"), in a form that has to be decoded rather than read, so the band
 * now starts at its first host and FleetPage's line carries both counts.
 */
export function AttentionBand({
  conditions,
  renderAction,
}: AttentionBandProps) {
  if (conditions.length === 0) return null;

  const groups = groupByHost(conditions);
  const visible = groups.slice(0, MAX_HOST_ROWS);
  const hiddenHostCount = groups.length - visible.length;

  return (
    /* Named, because the band no longer has a heading of its own: the "{n} on
       {m} hosts" <h3> was the landmark a screen-reader user navigated to, and
       the count that replaced it lives in a paragraph outside this element
       with no programmatic relationship to it. Same aria-label the host
       page's band carries, for the same reason. */
    <section className="attn" aria-label="Needs attention">
      {visible.map((group) => (
        <HostBlock
          key={group.hostId}
          group={group}
          action={renderAction?.(group)}
        />
      ))}
      {hiddenHostCount > 0 ? (
        <div className="attn-overflow">+{hiddenHostCount} more hosts →</div>
      ) : null}
    </section>
  );
}

function ConditionRow({ c }: { c: Condition }) {
  return (
    <li className="attn-cond">
      <Badge severity={c.severity}>{c.severity}</Badge>
      <span className="what">{c.what}</span>
      {c.since === null ? null : (
        <span className="since">{relative(c.since)}</span>
      )}
      {c.tab === null ? null : (
        <a className="drill" href={`/hosts/${c.hostId}/${c.tab}`}>
          {c.tab} →
        </a>
      )}
    </li>
  );
}

// `since` alone collides: one evaluation pass writes several conditions for
// a host with the same timestamp, which is the normal case rather than the
// edge one. `what` cannot help -- it is a ReactNode, and String() on an
// element is "[object Object]" for every one of them. The position within
// this host's own list is what actually distinguishes them, and it is stable
// for as long as the list is.
function rowKey(c: Condition, index: number): string {
  return `${c.since ?? "-"}#${index}`;
}

/**
 * One host and everything wrong with it.
 *
 * The host is a heading over its conditions rather than a row that happens to
 * carry one of them. That is the whole shape change: with the worst condition
 * promoted into the host's row, every OTHER condition had to be drawn as some
 * lesser thing -- an indented sub-row that lined up with no column above it
 * and read as a footnote to a sibling that was, in fact, its equal.
 */
function HostBlock({
  group,
  action,
}: {
  group: HostGroup;
  action?: ReactNode;
}) {
  const shown = group.conditions.slice(0, MAX_CONDITION_ROWS);
  const rest = group.conditions.slice(MAX_CONDITION_ROWS);
  const count = group.conditions.length;

  return (
    <section className="attn-host">
      <header>
        <a className="who" href={`/hosts/${group.hostId}/overview`}>
          {group.hostname}
        </a>
        <span className="count">
          {count} problem{count === 1 ? "" : "s"}
        </span>
        {action}
      </header>
      <ul>
        {shown.map((c, i) => (
          <ConditionRow key={rowKey(c, i)} c={c} />
        ))}
      </ul>
      {/* The disclosed rows live INSIDE the <details>, which is the whole
          contract of the element: the summary announces itself expanded and
          the thing it controls is what opens. They used to sit outside it,
          so a keyboard or screen-reader user got "+2 more, expanded" and an
          empty element, with the revealed rows floating as unrelated
          siblings. <details> also owns the open state itself -- the mirrored
          useState it replaced was a second copy of a fact the DOM already
          had. */}
      {rest.length > 0 ? (
        <details>
          <summary className="more">+{rest.length} more</summary>
          <ul>
            {rest.map((c, i) => (
              <ConditionRow key={rowKey(c, i)} c={c} />
            ))}
          </ul>
        </details>
      ) : null}
    </section>
  );
}
