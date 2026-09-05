import { describe, expect, it } from "vitest";
import {
  deriveState,
  isContainerStateKind,
  stateKindRank,
  FILTERABLE_STATE_KINDS,
} from "./state";

const NOW = new Date("2026-08-10T14:00:00Z");

describe("deriveState", () => {
  it("says no samples rather than reporting zero", () => {
    const state = deriveState({
      lastSampleMs: null,
      memUsed: null,
      memLimit: null,
      gap: false,
      now: NOW,
    });
    expect(state.label).toMatch(/no samples/i);
    expect(state.kind).toBe("no-samples");
  });

  it("calls a container that stopped appearing silent", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
    });
    expect(state.label).toMatch(/silent/i);
    expect(state.severity).toBe("serious");
  });

  // Memory approaching mem_limit is a warning (spec 11); a gap is reported
  // as a gap, never as the restart it probably was.
  it("warns on memory approaching mem_limit", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 60_000,
      memUsed: 9.6e8,
      memLimit: 1e9,
      gap: false,
      now: NOW,
    });
    expect(state.label).toMatch(/mem_limit/);
    expect(state.severity).toBe("warning");
  });

  it("reports a gap as a gap, not as a restart", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 60_000,
      memUsed: 1e8,
      memLimit: 1e9,
      gap: true,
      now: NOW,
    });
    expect(state.label).toMatch(/gap/i);
    expect(state.label).not.toMatch(/restart/i);
    expect(state.why).toMatch(/restart/i);
  });

  // The contradiction this branch exists to end: containerIsGone measures
  // against the host and marks nothing gone on an offline host, so the badge
  // must not call the same container Silent in the same breath.
  it("names the host, not the container, when the host stopped reporting", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "critical", label: "offline" },
    });
    expect(state.label).toBe("host offline");
    expect(state.kind).toBe("host-down");
  });

  // The host carries the severity on its own page and in its fleet row.
  // Repeating it here counts one outage twice.
  it("leaves the host's severity to the host", () => {
    const state = deriveState({
      lastSampleMs: null,
      memUsed: null,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "critical", label: "never seen" },
    });
    expect(state.severity).toBe("neutral");
    expect(state.label).toBe("host never seen");
  });

  // A host answering badly is still answering: its containers' own readings
  // are current, so the badge keeps measuring them.
  it("still judges the container when the host is merely sporadic", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "warning", label: "sporadic" },
    });
    expect(state.label).toMatch(/silent/i);
  });

  // A container measured gone stopped while its host was still posting, so
  // the host's later silence does not get to explain it away.
  it("keeps blaming the container when it stopped before its host did", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "critical", label: "offline" },
      gone: true,
    });
    expect(state.kind).toBe("gone");
  });

  // The two used to be one row's two labels: gone measures against the host's
  // last report and silent against the clock, so a gone container was always
  // also a silent one. Now gone is tested first and says so instead.
  it("says gone rather than silent for a container measured gone", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      gone: true,
    });
    expect(state.kind).toBe("gone");
    expect(state.label).toBe("gone");
    expect(state.severity).toBe("serious");
  });

  // And the window below it is still silent: quiet for four minutes on a host
  // that is still posting is not yet gone, and carries no purge button.
  it("still says silent while the container is only newly quiet", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 240_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      gone: false,
    });
    expect(state.kind).toBe("silent");
  });

  // The detail page's empty-window case: no series in the range, and the
  // reason there is none is that the container is gone. It outranks "No
  // samples" there, because the purge button below is offered on it.
  it("says gone over an empty window rather than no samples", () => {
    const state = deriveState({
      lastSampleMs: null,
      memUsed: null,
      memLimit: null,
      gap: false,
      now: NOW,
      gone: true,
    });
    expect(state.kind).toBe("gone");
  });

  // Without it, the same empty window is exactly what it looks like, because
  // containerIsGone returns false on exactly those rows -- a host whose agent
  // cannot see containers, or a timestamp that does not parse. Asserted here
  // so the branch order above stays honest about why it is safe.
  it("says no samples, not gone, when there is no timestamp to measure", () => {
    const state = deriveState({
      lastSampleMs: null,
      memUsed: null,
      memLimit: null,
      gap: false,
      now: NOW,
      gone: false,
    });
    expect(state.kind).toBe("no-samples");
  });
});

describe("state kinds", () => {
  // The URL carries the kind, so an unrecognised one has to be rejected
  // rather than trusted into a filter that then matches nothing.
  it("recognises its own kinds and nothing else", () => {
    expect(isContainerStateKind("silent")).toBe(true);
    expect(isContainerStateKind("host-down")).toBe(true);
    expect(isContainerStateKind("disk")).toBe(false);
    expect(isContainerStateKind("")).toBe(false);
  });

  // A filter names what is wrong: offering "Reporting" as a switch is the
  // 400-row inventory the list already is.
  it("offers no filter for a container that is fine", () => {
    expect(FILTERABLE_STATE_KINDS).not.toContain("reporting");
    expect(FILTERABLE_STATE_KINDS).toContain("silent");
  });

  // Sorting by status answers "which containers do I look at", so the state
  // that is not about this container ranks below the ones that are.
  it("ranks the container's own troubles above its host's", () => {
    expect(stateKindRank("silent")).toBeLessThan(stateKindRank("host-down"));
    expect(stateKindRank("mem-pressure")).toBeLessThan(
      stateKindRank("host-down"),
    );
    expect(stateKindRank("host-down")).toBeLessThan(stateKindRank("reporting"));
  });

  // Docker's own statement about a container still being sampled outranks an
  // inference from an absence: a failing healthcheck wants attention now,
  // while a container that stopped reporting may have been removed on purpose.
  it("ranks what Docker said above what was inferred from silence", () => {
    expect(stateKindRank("unhealthy")).toBeLessThan(stateKindRank("silent"));
    expect(stateKindRank("restarting")).toBeLessThan(stateKindRank("silent"));
    // Paused is not a fault, so it sits with host-down at the bottom.
    expect(stateKindRank("paused")).toBeGreaterThan(
      stateKindRank("mem-pressure"),
    );
  });
});

describe("deriveState with Docker's own answers", () => {
  const FRESH = {
    lastSampleMs: NOW.getTime() - 30_000,
    memUsed: 1,
    memLimit: null,
    gap: false,
    now: NOW,
  };

  it("quotes Docker's unhealthy rather than calling the container fine", () => {
    const state = deriveState({ ...FRESH, health: "unhealthy" });
    expect(state.kind).toBe("unhealthy");
    expect(state.severity).toBe("serious");
  });

  it("names a restarting container, which is a crash loop when it persists", () => {
    const state = deriveState({ ...FRESH, dockerState: "restarting" });
    expect(state.kind).toBe("restarting");
    expect(state.severity).toBe("serious");
  });

  // Somebody paused it. Its flat charts are the consequence, not a fault.
  it("calls a paused container paused, and not a problem", () => {
    const state = deriveState({ ...FRESH, dockerState: "paused" });
    expect(state.kind).toBe("paused");
    expect(state.severity).toBe("neutral");
  });

  // The commonest value on a real host: most images define no HEALTHCHECK.
  // It is a reading, and it must change nothing.
  it("treats health 'none' as no problem at all", () => {
    expect(deriveState({ ...FRESH, health: "none" }).kind).toBe("reporting");
  });

  // An agent with no Docker socket, or one older than the release that sends
  // these, leaves the derivation exactly as it was.
  it("is unchanged when Docker said nothing", () => {
    expect(
      deriveState({ ...FRESH, health: null, dockerState: null }).kind,
    ).toBe("reporting");
  });

  // These attributes rode in on the last sample, so a container that has gone
  // silent has nothing current to quote -- a "healthy" from an hour ago is not
  // a fact about it now.
  it("does not quote a stale health over a broken sample stream", () => {
    const silent = deriveState({
      ...FRESH,
      lastSampleMs: NOW.getTime() - 3_600_000,
      health: "unhealthy",
    });
    expect(silent.kind).toBe("silent");

    const hostDown = deriveState({
      ...FRESH,
      health: "unhealthy",
      hostState: { label: "offline", severity: "critical" },
    });
    expect(hostDown.kind).toBe("host-down");
  });
});

describe("a series gap and the restart counter", () => {
  const GAPPED = {
    lastSampleMs: NOW.getTime() - 30_000,
    memUsed: 1,
    memLimit: null,
    gap: true,
    now: NOW,
  };

  it("names the restarts when the counter rose across the window", () => {
    expect(deriveState({ ...GAPPED, restartsInWindow: 3 }).why).toMatch(
      /restarted the container 3 times/i,
    );
    expect(deriveState({ ...GAPPED, restartsInWindow: 1 }).why).toMatch(
      /restarted the container once/i,
    );
  });

  // The reading that used to be unavailable, and the one that matters: the
  // samples went missing and the container did NOT restart, so something
  // between the container and the hub lost them.
  it("says the container did not restart when the counter held still", () => {
    expect(deriveState({ ...GAPPED, restartsInWindow: 0 }).why).toMatch(
      /did not restart/i,
    );
  });

  // Every range answered from a rollup: restart_count lives in the raw table
  // only, so this knows exactly as much as it did before the counter existed
  // and must not claim otherwise.
  it("claims nothing about restarts when the range carries no counter", () => {
    const why = deriveState({ ...GAPPED, restartsInWindow: null }).why;
    expect(why).toMatch(/no restart count is available/i);
    expect(why).not.toMatch(/did not restart/i);
  });
});
