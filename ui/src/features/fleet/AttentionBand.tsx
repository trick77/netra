import type { ReactNode } from "react";
import { Badge, type Severity } from "../../ui/Badge";
import { relative } from "../../lib/format";

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
  since: string;
}

export interface HostGroup {
  hostId: string;
  hostname: string;
  /** Every condition for this host, in original order -- grouping is
   * presentation, never suppression, so nothing is dropped here. */
  conditions: Condition[];
  /** The single worst condition, used both as the display headline and as
   * this group's sort key. */
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
      conditions: hostConditions,
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
// fail. One or two conditions read fine flat, so the threshold is 2.
const GROUP_THRESHOLD = 2;

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

export function AttentionBand({
  conditions,
  renderAction,
}: AttentionBandProps) {
  if (conditions.length === 0) return null;

  const groups = groupByHost(conditions);
  const hostCount = groups.length;
  const conditionCount = conditions.length;
  const visible = groups.slice(0, MAX_HOST_ROWS);
  const hiddenHostCount = groups.length - visible.length;

  return (
    <div className="attn">
      <header>
        <h3>
          {conditionCount} on {hostCount} host{hostCount === 1 ? "" : "s"}
        </h3>
      </header>
      {visible.map((group) => (
        <HostRow
          key={group.hostId}
          group={group}
          action={renderAction?.(group)}
        />
      ))}
      {hiddenHostCount > 0 ? (
        <div className="attn-row">
          <span className="what">+{hiddenHostCount} more hosts →</span>
        </div>
      ) : null}
    </div>
  );
}

function ConditionSubRow({ c }: { c: Condition }) {
  return (
    <div className="attn-sub">
      <Badge severity={c.severity}>{c.severity}</Badge>
      <span className="what">{c.what}</span>
      <span className="since">{relative(c.since)}</span>
    </div>
  );
}

// `since` alone collides: one evaluation pass writes several conditions for
// a host with the same timestamp, which is the normal case rather than the
// edge one. `what` cannot help -- it is a ReactNode, and String() on an
// element is "[object Object]" for every one of them. The position within
// this host's own list is what actually distinguishes them, and it is stable
// for as long as the list is.
function subRowKey(c: Condition, index: number): string {
  return `${c.since}#${index}`;
}

function HostRow({ group, action }: { group: HostGroup; action?: ReactNode }) {
  const rest = group.conditions.filter((c) => c !== group.worst);
  // Above the threshold, everything but the worst condition hides behind
  // an explicit disclosure -- one cascading failure on a single host must
  // not flood the band and bury a second host quietly starting to fail.
  const grouped = group.conditions.length > GROUP_THRESHOLD;

  return (
    <>
      <div className="attn-row">
        <a className="who" href={`/hosts/${group.hostId}/overview`}>
          {group.hostname}
        </a>
        <Badge severity={group.worst.severity}>{group.worst.severity}</Badge>
        <span className="what">{group.worst.what}</span>
        <span className="since">{relative(group.worst.since)}</span>
        {action}
      </div>
      {/* The disclosed rows live INSIDE the <details>, which is the whole
          contract of the element: the summary announces itself expanded and
          the thing it controls is what opens. They used to sit outside it,
          so a keyboard or screen-reader user got "+2 more, expanded" and an
          empty element, with the revealed rows floating as unrelated
          siblings. <details> also owns the open state itself -- the mirrored
          useState it replaced was a second copy of a fact the DOM already
          had. */}
      {grouped ? (
        <details>
          <summary className="more">+{rest.length} more</summary>
          {rest.map((c, i) => (
            <ConditionSubRow key={subRowKey(c, i)} c={c} />
          ))}
        </details>
      ) : (
        rest.map((c, i) => <ConditionSubRow key={subRowKey(c, i)} c={c} />)
      )}
    </>
  );
}
