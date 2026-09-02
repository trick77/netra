import { describe, expect, it } from "vitest";
import { containerHeadline, hostHeadline } from "./headline";

/** The sentence as it is read, which is what these are really about. */
function read(head: ReturnType<typeof hostHeadline>): string {
  const tail = head.clause ?? head.steady;
  return tail === null ? head.stem : `${head.stem}, ${tail}`;
}

describe("hostHeadline", () => {
  it("states the fleet, then what is wrong with it", () => {
    expect(read(hostHeadline(4, 2))).toBe("4 hosts, 2 need attention");
  });

  it("swaps only the tail on an all-clear fleet", () => {
    expect(read(hostHeadline(4, 0))).toBe("4 hosts, all steady");
  });

  // "1 of 1 host needs attention" counts a set of one, and "All 1 host is
  // steady" is what a plural rule produces rather than English. The clause
  // agrees with what it counts, so neither wording is reachable.
  describe("a fleet of one", () => {
    it("does not count a set of one", () => {
      expect(read(hostHeadline(1, 1))).toBe("1 host, needs attention");
    });

    it("has no 'all' to be steady", () => {
      expect(read(hostHeadline(1, 0))).toBe("1 host, steady");
    });
  });

  // Before the first fetch lands there is no set to state, and an empty
  // fleet's own empty state says "No hosts yet" directly below this line.
  it("falls back to naming the page when there is no host yet", () => {
    const head = hostHeadline(0, 0);
    expect(head.stem).toBe("Hosts");
    expect(head.clause).toBeNull();
    expect(head.steady).toBeNull();
  });

  it("puts the severity's colour on the clause and not the whole line", () => {
    const head = hostHeadline(4, 2);
    expect(head.stem).toBe("4 hosts");
    expect(head.clause).toBe("2 need attention");
  });
});

describe("containerHeadline", () => {
  // Measurement language, not advice: nothing is being asked of a container,
  // and "reporting" is the word its states already turn on.
  it("says what was measured rather than what to do", () => {
    expect(read(containerHeadline(22, 3, true))).toBe(
      "22 containers, 3 not reporting normally",
    );
  });

  it("swaps only the tail when every container is reporting", () => {
    expect(read(containerHeadline(22, 0, true))).toBe(
      "22 containers, all reporting",
    );
  });

  it("agrees with itself over a single container", () => {
    expect(read(containerHeadline(1, 1, true))).toBe(
      "1 container, not reporting normally",
    );
    expect(read(containerHeadline(1, 0, true))).toBe("1 container, reporting");
  });

  // The fan-out has not answered yet. Zero is a claim -- that this fleet runs
  // no containers -- and it is not one an unanswered fetch can make.
  it("states nothing while the containers are unknown", () => {
    expect(read(containerHeadline(0, 0, false))).toBe("Containers");
    expect(read(containerHeadline(0, 0, true))).toBe("Containers");
  });

  // One host answered 500, so the list is short by however many containers
  // that host runs. It can still say how many of the rows it HAS are unwell;
  // what it cannot do is call the fleet's containers all healthy above a note
  // saying a host went unasked.
  describe("a fan-out that lost a host", () => {
    it("withholds the all-clear it has no basis for", () => {
      expect(read(containerHeadline(18, 0, true, false))).toBe("18 containers");
    });

    it("still counts the ones it can see", () => {
      expect(read(containerHeadline(18, 2, true, false))).toBe(
        "18 containers, 2 not reporting normally",
      );
    });
  });
});
