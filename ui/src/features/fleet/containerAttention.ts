import type { ReactNode } from "react";
import type { AttentionRow } from "../../ui/AttentionBand";
import type { ContainerRow } from "../container/columns";
import { containerState } from "../container/columns";
import {
  FILTERABLE_STATE_KINDS,
  type ContainerStateKind,
} from "../container/state";

// What is wrong with the fleet's containers, as one line each.
//
// The vocabulary is deriveState's and nothing here invents a second one: the
// sentence a band prints and the badge the row carries are the same judgement,
// so a reader who scrolls from one to the other is not told two things.
//
// ORDER is FILTERABLE_STATE_KINDS, which is the ranked order the chips and the
// Status column already use. The band partitions by severity and preserves
// what it is given, so writing them in rank order is what puts the worst kind
// at the top of each severity.

/**
 * The onset of a container's state, where one genuinely exists.
 *
 * Four kinds can answer and three cannot, and the split is not arbitrary:
 *
 *   - `restarting` and `paused` are Docker STATES, and containers.state_ts
 *     records when the state was entered.
 *   - `silent` and `gone` are measured from last_seen, which IS the moment the
 *     samples stopped.
 *   - `unhealthy`, `mem-pressure` and `series-gap` have nothing. `health`
 *     carries no timestamp on the wire, and the other two are derived from the
 *     row on every render, so they have no memory of when they first became
 *     true.
 *
 * The three that cannot answer print no clause at all. That is the same rule
 * conditions.ts follows for `sporadic`, and it matters: "since 5 m ago" beside
 * something that has been wrong for a week is a sentence someone acts on.
 * Closing the gap properly means container conditions in the hub, the way hosts
 * got them -- a browser derivation cannot remember.
 */
function onsetOf(row: ContainerRow, kind: ContainerStateKind): string | null {
  switch (kind) {
    case "restarting":
    case "paused":
      return row.state_since ?? null;
    case "silent":
    case "gone":
      return row.last_seen;
    default:
      return null;
  }
}

export interface ContainerAttentionOptions {
  now?: Date;
  /** Builds the sentence. Passed in so this module never renders a link and
   * never needs to know how the page routes. */
  sentence: (row: ContainerRow, why: string) => ReactNode;
}

/**
 * One band row per container that is not simply reporting.
 *
 * `reporting` and `no-samples` are both excluded, and for different reasons.
 * The first is healthy. The second is nobody having looked -- a host whose
 * agent cannot read cgroup scopes reports no container sample at all, and
 * listing every container on it as a problem would fill the band with one
 * host's blind spot. The list below already says why that host is quiet.
 */
export function containerAttention(
  rows: readonly ContainerRow[],
  { now = new Date(), sentence }: ContainerAttentionOptions,
): AttentionRow[] {
  const out: AttentionRow[] = [];
  // By kind, in rank order, so the band reads worst-first inside each severity
  // rather than in whatever order the fan-out happened to return hosts.
  for (const kind of FILTERABLE_STATE_KINDS) {
    for (const row of rows) {
      const state = containerState(row, now);
      if (state.kind !== kind) continue;
      out.push({
        severity: state.severity,
        what: sentence(row, state.why),
        since: onsetOf(row, kind),
      });
    }
  }
  return out;
}

/**
 * How many rows of a severity the fleet's band will draw before it stops.
 *
 * Five. A host has a bounded number of things wrong with it and its own band
 * needs no cap; a FLEET does not, and one lossy host can put a `series-gap`
 * row in here for every container it runs. Unbounded, this is the per-item
 * band conditions.ts deleted for hosts -- and the overflow line is a link,
 * because that band's actual failure was "+30 more hosts" with nothing to
 * click.
 */
export const ATTENTION_CAP = 5;
