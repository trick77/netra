// The ONE container state derivation. Three surfaces render it -- the detail
// page's header badge, the fleet Containers tab and a host's own Containers
// tab -- and they render the same words for the same reason columns.tsx is
// one column set: two lists that describe the same container in different
// vocabularies is how "gone" and "Silent" came to name one fact.
//
// "Gone" is one of those words now rather than a pill beside them. It was a
// boolean carried alongside the state, and the two are not independent: gone
// measures container.last_seen against its HOST's last report and silent
// measures it against the clock, and since a host's last report is never in
// the future, every gone container was also Silent. One row said both, and
// the fleet's Silent chip counted containers whose own row called them gone.
//
// It lived in ContainerPage while only that page had a badge. The lists could
// not call it then -- the comment in columns.tsx said so -- because it needs
// sample timestamps and the fleet fan-out was believed not to have them. It
// does: containers.last_seen rides on every listing row, and both lists build
// the cpu/mem series a ContainerRow carries. Nothing about the derivation is
// page-specific, so it moved here rather than being copied.
//
// Every label names a MEASUREMENT: samples arrived, samples stopped, memory
// approached its limit, the series has a hole, Docker called the container
// unhealthy. The likely cause goes in `why`, never in the label.
//
// That last one is new, and it is the first thing here that is not derived. It
// used to say restarts reach neither the wire nor the schema, so "Series gap"
// refused to name a restart it could not measure; the agent now reads state,
// health and restart counts off the Docker socket and they arrive on the
// container row. Where Docker has SAID something, this stops inferring and
// quotes it -- but only where it can, and only while it is fresh: these
// attributes ride in on a sample, so a container that has gone silent has
// nothing current to quote and the derived branches still own it.
import type { Severity } from "../../ui/Badge";
import type { HostStatus } from "../../lib/host";

/** A container is silent once it has missed this many seconds of samples.
 * Three scrape intervals at the 60s default: one missed post is a hiccup,
 * three in a row is the container no longer being there. */
export const SILENT_AFTER_S = 180;

/** Memory this close to mem_limit is the warning spec 11 asks for -- the
 * OOM killer arrives before the bar reaches the end of its track. */
export const MEM_PRESSURE_PCT = 90;

/**
 * What a state IS, as opposed to what it is called.
 *
 * The kind is what the counts line groups by and what `?attn=` carries, the
 * same split conditions.ts makes for hosts. Filtering on the label instead
 * would put a display string in the URL and break the filter the day the
 * wording changes -- and `host-down`'s label is not even constant, since it
 * quotes the host's own word.
 */
export type ContainerStateKind =
  | "host-down"
  | "no-samples"
  | "gone"
  | "silent"
  | "unhealthy"
  | "restarting"
  | "paused"
  | "mem-pressure"
  | "series-gap"
  | "reporting";

export interface DerivedState {
  kind: ContainerStateKind;
  label: string;
  severity: Severity;
  /** The inference the label deliberately does not make. */
  why: string;
}

export interface DerivedStateInput {
  lastSampleMs: number | null;
  memUsed: number | null;
  memLimit: number | null;
  gap: boolean;
  now: Date;
  /**
   * The HOST's reporting state, from the one hostStatus() the fleet and the
   * host header already share.
   *
   * A container's samples ride in on its host's posts, so a host that went
   * quiet stops every container on it at once. Measuring silence against the
   * clock on such a host blames the container for its host's outage -- and it
   * did, calling a container Silent above a button offering to purge it.
   *
   * Given the host's state, the badge names the host instead. Omitted, the
   * badge behaves as it did: callers without a host status are no worse off
   * than before.
   */
  hostState?: HostStatus;
  /**
   * containerIsGone's answer: this container stopped being reported WHILE
   * its host kept reporting.
   *
   * The input to the `gone` state, and it outranks hostState, because it is a
   * fact about the container that was established before the host went
   * anywhere -- and because the page offers a purge button on exactly this
   * condition. Without it, a container that died an hour before its host did
   * would carry a "Host offline" badge saying nothing can be said about it,
   * above a button offering to delete its history.
   *
   * Measured against the host and never against the clock, so an offline host
   * marks nothing gone; see containerIsGone in columns.tsx for why that
   * direction is the safe one to fail in.
   */
  gone?: boolean;
  silentAfterS?: number;
  /**
   * Docker's own word for what the container is doing, from
   * containers.docker_state.
   *
   * null is "not reported" -- no socket, or an agent older than the release
   * that sends it -- and every branch below treats it as saying nothing rather
   * than as saying "running". An agent that cannot see Docker leaves this page
   * exactly as it was before Docker was read at all.
   */
  dockerState?: string | null;
  /**
   * Docker's health, from containers.health.
   *
   * "none" is the commonest value on a real host and means the image defines
   * no HEALTHCHECK. It is a measurement and NOT a problem, so it changes
   * nothing here -- only "unhealthy" does.
   */
  health?: string | null;
  /**
   * How much Docker's restart counter rose across the window on screen, or
   * null where it cannot be known.
   *
   * It is the difference, not the total: this exists to explain a hole in the
   * series, and "this container has restarted 31 times since it was created"
   * does not say whether it restarted during the gap being looked at. Null
   * where the series has no restart_count at all, which is every range served
   * from a rolled-up tier -- the column lives only in the raw table.
   */
  restartsInWindow?: number | null;
}

export function deriveState({
  lastSampleMs,
  memUsed,
  memLimit,
  gap,
  now,
  hostState,
  gone = false,
  silentAfterS = SILENT_AFTER_S,
  dockerState = null,
  health = null,
  restartsInWindow = null,
}: DerivedStateInput): DerivedState {
  // First, above every branch that reads the sample stream, because this one
  // says the stream STOPPED while the host kept posting -- which is the fact
  // the purge button is offered on, and the strongest thing that can be said
  // about a container that is not there.
  //
  // Above "No samples" too, which only the detail page can reach: a container
  // gone for two hours, read at the 1h range, has an empty window BECAUSE it
  // is gone, and "No samples" over a purge button says less than the button
  // does. A listing row cannot arrive here without a timestamp at all --
  // containerIsGone returns false when one does not parse and on a host whose
  // agent cannot see containers, which are the two ways a row loses it.
  //
  // Serious rather than neutral. A container that crashed at 03:00 and never
  // came back is not a settled administrative fact at 03:15; it is the same
  // problem it was at 03:04, when the row still read Silent.
  if (gone) {
    return {
      kind: "gone",
      label: "Gone",
      severity: "serious",
      why: "it stopped being reported while its host kept reporting; it was probably stopped or removed",
    };
  }

  // Then, above even "No samples": every branch below reads the sample
  // stream, and on a host that is not reporting there is no stream to read.
  // Neutral, not serious -- the severity belongs to the host, which carries
  // it on its own page and in its fleet row, and a second critical here would
  // count one outage twice. The host's own word is reused rather than a fifth
  // synonym invented for it.
  //
  // Below `gone` for the reason that branch gives: a container that died
  // while its host was still posting is measured, and the host going quiet
  // afterwards does not unmeasure it.
  if (hostState !== undefined && hostState.severity === "critical") {
    return {
      kind: "host-down",
      label: `Host ${hostState.label}`,
      severity: "neutral",
      why: "the host stopped reporting, so nothing can be said about this container until it comes back",
    };
  }

  if (lastSampleMs === null) {
    return {
      kind: "no-samples",
      label: "No samples",
      severity: "neutral",
      why: "nothing has been reported for this container in the selected range",
    };
  }

  // Quiet, but not yet long enough for the host to have noticed it missing --
  // `gone` above owns that, at GONE_AFTER_S. So this is now the narrow window
  // between three minutes of missed posts and fifteen: something that has
  // just stopped, rather than something that is not coming back. The `why`
  // no longer guesses "removed", because the state that means removed is a
  // different one.
  const ageS = (now.getTime() - lastSampleMs) / 1000;
  if (ageS > silentAfterS) {
    return {
      kind: "silent",
      label: "Silent",
      severity: "serious",
      why: "samples stopped arriving for this container while its host kept reporting",
    };
  }

  // Docker's own answers, and they sit HERE rather than at the top on purpose.
  //
  // Every one of them arrived on a sample, so they are exactly as fresh as the
  // sample stream is. Above this line the stream is broken -- the host is
  // gone, nothing was ever reported, the container stopped reporting -- and a
  // "healthy" from twenty minutes ago is not a fact about the container now.
  // The three branches above own those cases; this one only speaks while the
  // samples are current.
  //
  // Below this line the derivation continues as it always did, so a fleet whose
  // agents cannot read the socket is exactly as informative as it was before.
  if (dockerState === "restarting") {
    return {
      kind: "restarting",
      label: "Restarting",
      severity: "serious",
      why: "Docker reports the container as restarting, which on a container that stays in this state is a crash loop",
    };
  }

  if (health === "unhealthy") {
    return {
      kind: "unhealthy",
      label: "Unhealthy",
      severity: "serious",
      why: "the container's own HEALTHCHECK is failing; the samples below are what it is doing while it fails",
    };
  }

  if (dockerState === "paused") {
    // Neutral, not a warning. A paused container is one somebody paused, and
    // its flat charts are the consequence rather than a second problem.
    return {
      kind: "paused",
      label: "Paused",
      severity: "neutral",
      why: "the container is paused, so its processes are frozen and its counters do not move",
    };
  }

  if (
    memUsed !== null &&
    memLimit !== null &&
    memLimit > 0 &&
    (memUsed / memLimit) * 100 >= MEM_PRESSURE_PCT
  ) {
    return {
      kind: "mem-pressure",
      label: "Near mem_limit",
      severity: "warning",
      why: "memory is approaching the configured limit; the OOM killer acts before the limit is reached",
    };
  }

  if (gap) {
    // The one place the restart counter earns its keep. A hole in the series
    // has always LOOKED like a restart and this could never say so; now it can
    // check, and it says which of the three cases it is rather than picking
    // the likeliest.
    return {
      kind: "series-gap",
      label: "Series gap",
      severity: "warning",
      why:
        restartsInWindow === null
          ? // No restart counter for this window: it lives only in the raw
            // tier, so any range answered from a rollup lands here. Same
            // wording as before the counter existed, because the same amount
            // is known.
            "a hole in the series usually means a restart, but no restart count is available for this range, so this says only that samples are missing"
          : restartsInWindow > 0
            ? `samples are missing, and Docker restarted the container ${restartsInWindow === 1 ? "once" : `${restartsInWindow} times`} in this window`
            : "samples are missing, and the container did not restart -- so the agent, the host or the hub lost them rather than the container going away",
    };
  }

  return {
    kind: "reporting",
    label: "Reporting",
    severity: "ok",
    why: "samples are arriving on schedule",
  };
}

/**
 * The kinds a reader can filter a container list down to, worst first.
 *
 * `reporting` is not here and neither is `no-samples`: a filter names what is
 * WRONG, and "show me the 400 containers that are fine" is the inventory the
 * list already is. `host-down` stays despite being neutral, because "which of
 * my containers can netra not currently see" is a real question and the
 * answer is otherwise spread across however many hosts went quiet.
 *
 * `gone` sits after `silent` and not before it, which is the opposite of the
 * order deriveState tests them in. Precedence answers "what is this row",
 * where gone is the more specific fact; this answers "what does a reader
 * look at first", and there silent is the three-to-fifteen-minute window
 * where something is still happening, while gone is settled enough to have a
 * purge button on it.
 */
export const FILTERABLE_STATE_KINDS: readonly ContainerStateKind[] = [
  "unhealthy",
  "restarting",
  "silent",
  "gone",
  "mem-pressure",
  "series-gap",
  "paused",
  "host-down",
];

/**
 * The tile label for a kind.
 *
 * Not deriveState's own label, for one kind only: `host-down` renders as
 * "Host offline" or "Host never seen" depending on which host a row sits on,
 * and a counts tile spans hosts that may disagree. It says the condition
 * rather than any one row's wording.
 */
const KIND_LABEL: Record<ContainerStateKind, string> = {
  "host-down": "Host not reporting",
  "no-samples": "No samples",
  gone: "Gone",
  silent: "Silent",
  unhealthy: "Unhealthy",
  restarting: "Restarting",
  paused: "Paused",
  "mem-pressure": "Near mem_limit",
  "series-gap": "Series gap",
  reporting: "Reporting",
};

export function stateKindLabel(kind: ContainerStateKind): string {
  return KIND_LABEL[kind];
}

/**
 * A kind's severity, for a counts tile that spans many rows.
 *
 * The same severity deriveState gives a row in that state -- stated once here
 * because a tile has no row to ask, not because it is a second opinion.
 */
const KIND_SEVERITY: Record<ContainerStateKind, Severity> = {
  "host-down": "neutral",
  "no-samples": "neutral",
  gone: "serious",
  silent: "serious",
  unhealthy: "serious",
  restarting: "serious",
  paused: "neutral",
  "mem-pressure": "warning",
  "series-gap": "warning",
  reporting: "ok",
};

export function stateKindSeverity(kind: ContainerStateKind): Severity {
  return KIND_SEVERITY[kind];
}

export function isContainerStateKind(
  value: string,
): value is ContainerStateKind {
  return Object.prototype.hasOwnProperty.call(KIND_LABEL, value);
}

/**
 * Worst first, for sorting a status column.
 *
 * `host-down` ranks below the container's own troubles on purpose: it is the
 * one state that is not about this container, and a reader sorting a list by
 * status is asking which containers to look at.
 *
 * `unhealthy` and `restarting` outrank both, because they are Docker's own
 * statement about a container that is STILL being sampled, while silent and
 * gone are inferences from an absence: a failing healthcheck is a container
 * asking for attention now, and a container that stopped reporting may simply
 * have been removed on purpose.
 *
 * `silent` above `gone` for the reason FILTERABLE_STATE_KINDS gives: newly
 * quiet before long gone, whatever their precedence in the derivation.
 *
 * `paused` sits with `host-down` near the bottom for the same reason that one
 * does -- somebody paused it, so it is not a fault to go and look at.
 */
const KIND_RANK: Record<ContainerStateKind, number> = {
  unhealthy: 0,
  restarting: 1,
  silent: 2,
  gone: 3,
  "mem-pressure": 4,
  "series-gap": 5,
  paused: 6,
  "host-down": 7,
  "no-samples": 8,
  reporting: 9,
};

export function stateKindRank(kind: ContainerStateKind): number {
  return KIND_RANK[kind];
}

/**
 * The kinds that quote Docker rather than infer from the sample stream.
 *
 * The detail page captions its badge with which of the two it is, because how
 * much a reader should trust "Unhealthy" and how much they should trust
 * "Series gap" are different questions -- one is the daemon's own answer, the
 * other is netra reading a shape.
 */
export const DOCKER_STATED_KINDS: ReadonlySet<ContainerStateKind> = new Set([
  "unhealthy",
  "restarting",
  "paused",
]);
