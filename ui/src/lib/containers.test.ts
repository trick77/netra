import { describe, expect, it } from "vitest";
import { fleetContainerNotes, hostContainerNote } from "./containers";

const cgroups = { containers: "no-cgroup-scopes" };
const socket = { containers: "no-docker-socket" };

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
});
