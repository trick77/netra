import { describe, expect, it } from "vitest";
import { containerAttention } from "./containerAttention";
import type { ContainerRow } from "../container/columns";

const NOW = new Date("2026-08-10T14:00:00Z");

function makeRow(overrides: Partial<ContainerRow> = {}): ContainerRow {
  return {
    id: 1,
    container_key: "shop/web",
    name: "shop-web-1",
    image: "nginx:1.27",
    is_agent: false,
    docker_state: null,
    health: null,
    state_since: null,
    started_at: null,
    restarts_window: 0,
    recreates_window: 0,
    last_restart: null,
    restarts_window_seconds: 86400,
    restart_count: null,
    labels: null,
    // Current, and on a host that is current: a healthy row unless something
    // below says otherwise.
    last_seen: "2026-08-10T14:00:00Z",
    host_last_seen: "2026-08-10T14:00:00Z",
    host_id: 7,
    hostname: "web-01",
    ...overrides,
  };
}

const sentence = (row: ContainerRow, why: string) => `${row.name}: ${why}`;

describe("containerAttention", () => {
  it("says nothing about a fleet where everything is reporting", () => {
    expect(
      containerAttention([makeRow(), makeRow({ id: 2 })], {
        now: NOW,
        sentence,
      }),
    ).toEqual([]);
  });

  // A host whose agent cannot read cgroup scopes reports no container sample
  // at all. Listing every container on it would fill the band with one host's
  // blind spot, and the list below already says why that host is quiet.
  it("says nothing about containers nobody could look at", () => {
    const rows = [
      makeRow({
        last_seen: "2026-08-10T09:00:00Z",
        host_containers_capability: "no-cgroup-scopes",
      }),
    ];
    expect(containerAttention(rows, { now: NOW, sentence })).toEqual([]);
  });

  it("writes one row per container that is not simply reporting", () => {
    const rows = [
      makeRow(),
      makeRow({ id: 2, name: "sick", health: "unhealthy" }),
      makeRow({ id: 3, name: "quiet", last_seen: "2026-08-10T13:50:00Z" }),
    ];

    const got = containerAttention(rows, { now: NOW, sentence });

    expect(got).toHaveLength(2);
    expect(got.map((row) => row.severity)).toEqual(["critical", "warning"]);
  });

  // FILTERABLE_STATE_KINDS order, which is the ranked order the chips and the
  // Status column already use -- so the band reads worst-first rather than in
  // whatever order the fan-out returned hosts.
  it("writes the rows in rank order, not in row order", () => {
    const rows = [
      makeRow({ id: 1, name: "quiet", last_seen: "2026-08-10T13:50:00Z" }),
      makeRow({ id: 2, name: "sick", health: "unhealthy" }),
    ];

    const got = containerAttention(rows, { now: NOW, sentence });

    expect(got[0]!.what).toContain("sick");
    expect(got[1]!.what).toContain("quiet");
  });

  describe("the onset", () => {
    // Docker states it outright, so the row can.
    it("dates a restarting container from its state timestamp", () => {
      const got = containerAttention(
        [
          makeRow({
            docker_state: "restarting",
            state_since: "2026-08-10T13:38:00Z",
          }),
        ],
        { now: NOW, sentence },
      );
      expect(got[0]!.since).toBe("2026-08-10T13:38:00Z");
    });

    // last_seen IS the moment the samples stopped.
    it("dates a silent container from when its samples stopped", () => {
      const got = containerAttention(
        [makeRow({ last_seen: "2026-08-10T13:50:00Z" })],
        { now: NOW, sentence },
      );
      expect(got[0]!.since).toBe("2026-08-10T13:50:00Z");
    });

    // The three that cannot answer must print nothing rather than a plausible
    // instant: health carries no timestamp on the wire, and the other two are
    // derived per render and have no memory of when they became true.
    it("gives no onset to the kinds that genuinely have none", () => {
      const unhealthy = containerAttention([makeRow({ health: "unhealthy" })], {
        now: NOW,
        sentence,
      });
      expect(unhealthy[0]!.since).toBeNull();

      const pressured = containerAttention(
        [makeRow({ mem: [950], mem_limit_bytes: 1000 })],
        { now: NOW, sentence },
      );
      expect(pressured[0]!.since).toBeNull();
    });
  });
});
