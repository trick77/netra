import { describe, expect, it } from "vitest";
import {
  fleetContainerNotes,
  fleetContainersBlocked,
  hostContainerNote,
  hostContainersBlocked,
} from "./containers";

const cgroups = { containers: "no-cgroup-scopes" };
const socket = { containers: "no-docker-socket" };
const silent = { containers: "docker-socket-silent" };

describe("hostContainerNote", () => {
  // The healthy case is the common one, and a note on every host page would
  // train people to ignore the ones that matter.
  it("says nothing when the agent reported nothing", () => {
    expect(hostContainerNote(undefined)).toBeNull();
    expect(hostContainerNote({})).toBeNull();
    expect(hostContainerNote({ smart: "no-device-access" })).toBeNull();
  });

  // "ok" is how several collectors report success (procs.go, users.go), and a
  // success is not an explanation.
  it("says nothing for ok, or for a key present but empty", () => {
    expect(hostContainerNote({ containers: "ok" })).toBeNull();
    expect(hostContainerNote({ containers: "" })).toBeNull();
  });

  it("names the mount and the script for no-cgroup-scopes", () => {
    const note = hostContainerNote(cgroups);
    // The remedy is the only actionable half. A note that describes the
    // symptom and stops is a note that changes nothing.
    expect(note).toContain("/host/sys/fs/cgroup");
    expect(note).toContain("setup-agent.sh");
    expect(note).toContain("no container metrics reached the hub");
  });

  // The milder one, and the wording has to stay careful: a host with no
  // Docker installed has no socket either and reports this with an EMPTY
  // list, so the sentence must not claim containers exist.
  it("explains raw ids for no-docker-socket without claiming containers exist", () => {
    const note = hostContainerNote(socket);
    expect(note).toContain("raw id");
    expect(note).toContain("Docker socket");
    expect(note).toContain("any containers it collects");
    expect(note).not.toContain("setup-agent.sh");
  });

  // The third state, and the one this list used to render as fifty-five rows
  // named by 64 hex characters: the socket IS there and named nothing, so the
  // agent reports nothing rather than minting an id-keyed container per
  // outage. The sentence has to say what is missing AND what to do about it.
  it("names the remedy for docker-socket-silent without mentioning raw ids", () => {
    const note = hostContainerNote(silent);
    expect(note).toContain("named no containers");
    expect(note).toContain("are not reported");
    expect(note).toContain("readable by the agent");
    // The raw-id fallback is exactly what this state no longer does.
    expect(note).not.toContain("raw id");
  });

  // capabilities is free-text JSONB with no enum and no CHECK, so a value
  // added to the agent tomorrow reaches this UI today.
  it("quotes a capability it does not know the wording of", () => {
    expect(hostContainerNote({ containers: "some-future-thing" })).toContain(
      "“some-future-thing”",
    );
  });
});

describe("fleetContainerNotes", () => {
  const partial = { partial: true };
  const empty = { partial: false };

  it("says nothing about a healthy fleet", () => {
    expect(
      fleetContainerNotes(
        [{ hostname: "a", capabilities: {} }, { hostname: "b" }],
        empty,
      ),
    ).toEqual([]);
  });

  // The case an empty-state-only fix misses entirely: eleven hosts reporting
  // containers and one that cannot. The list is full, looks complete, and is
  // short by a whole host.
  it("calls a mixed fleet's list incomplete, and names the host", () => {
    const [note] = fleetContainerNotes(
      [
        { hostname: "web-01", capabilities: {} },
        { hostname: "web-02", capabilities: cgroups },
      ],
      partial,
    );
    expect(note).toContain("This list is incomplete.");
    expect(note).toContain("web-02");
    expect(note).not.toContain("web-01");
  });

  // With nothing in the list at all, the empty state already frames it as
  // "nothing here" -- saying "incomplete" on top of that is noise.
  it("drops the incomplete framing when there is no list to be short", () => {
    const [note] = fleetContainerNotes(
      [{ hostname: "web-02", capabilities: cgroups }],
      empty,
    );
    expect(note).not.toContain("incomplete");
    expect(note).toContain("setup-agent.sh");
  });

  it("gathers hosts reporting the same thing into one sentence", () => {
    const notes = fleetContainerNotes(
      [
        { hostname: "web-02", capabilities: cgroups },
        { hostname: "web-01", capabilities: cgroups },
      ],
      partial,
    );
    expect(notes).toHaveLength(1);
    // Sorted, so two renders of the same fleet read identically.
    expect(notes[0]).toContain("web-01 and web-02");
  });

  it("keeps the two capabilities apart, one sentence each", () => {
    const notes = fleetContainerNotes(
      [
        { hostname: "tiny", capabilities: socket },
        { hostname: "broken", capabilities: cgroups },
      ],
      partial,
    );
    expect(notes).toHaveLength(2);
    expect(notes.join(" ")).toContain("broken");
    expect(notes.join(" ")).toContain("tiny");
    // Only the missing one is a claim about completeness. The unnamed one is
    // a list that is all there and badly labelled.
    expect(notes.filter((n) => n.includes("incomplete"))).toHaveLength(1);
  });

  it("lists three hosts with the serial comma the sentence needs", () => {
    const [note] = fleetContainerNotes(
      [
        { hostname: "c", capabilities: cgroups },
        { hostname: "a", capabilities: cgroups },
        { hostname: "b", capabilities: cgroups },
      ],
      partial,
    );
    expect(note).toContain("a, b and c");
  });

  // Same argument as no-cgroup-scopes: the agent measured containers and
  // reported none of them, so a list that looks complete is short by a host.
  it("calls the list incomplete for docker-socket-silent too", () => {
    const [note] = fleetContainerNotes(
      [
        { hostname: "web-01", capabilities: {} },
        { hostname: "web-02", capabilities: silent },
      ],
      partial,
    );
    expect(note).toContain("This list is incomplete.");
    expect(note).toContain("web-02");
  });
});

// Whether the empty list is a FAULT or a FACT. It decides what the two stacked
// Docker panels draw, and getting it wrong either alarms about a NAS running
// no containers or draws a host whose docker is down as a host at rest.
describe("containers blocked", () => {
  it("treats a silent socket as nothing collected", () => {
    expect(hostContainersBlocked(silent)).toBe(true);
    expect(
      fleetContainersBlocked([{ hostname: "a", capabilities: silent }]),
    ).toBe(true);
  });

  // The socket that was never mounted is the healthy case: whatever IS
  // collected still arrives, badly named.
  it("leaves an unmounted socket and a healthy host alone", () => {
    expect(hostContainersBlocked(socket)).toBe(false);
    expect(hostContainersBlocked(undefined)).toBe(false);
    expect(hostContainersBlocked(cgroups)).toBe(true);
  });
});
