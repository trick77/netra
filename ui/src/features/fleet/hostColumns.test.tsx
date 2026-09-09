// hostColumns() is the single source of truth for what a "host row" looks
// like: HostTable (Task 12) renders the Column<HostRow>[] this file
// produces, so a column added here cannot go missing from the page. These
// tests pin the contract it depends on: column order, the disk cell's
// fullest-mount naming, and the
// sub-300s uptime severity -- see task-11-brief.md.
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { areaPath, linePath } from "../../ui/charts/geometry";
import { SPARK_STRIP_HEIGHT, SPARK_WIDTH } from "../../ui/charts/size";
import { diskAxis, hostColumns, type HostRow } from "./hostColumns";
import { ABSENT, absolute } from "../../lib/format";

function makeRow(overrides: Partial<HostRow> = {}): HostRow {
  return {
    id: 1,
    hostname: "web-01",
    window: null,
    last_seen: "2026-08-10T13:59:30Z",
    cpu_total: 42,
    mem_used: 4_000_000_000,
    mem_total: 16_000_000_000,
    uptime_s: 864_000,
    threads: null,
    cpu: [
      { name: "user", color: "var(--s1)", values: [10, 12, 11] },
      { name: "system", color: "var(--s2)", values: [5, 4, 6] },
      { name: "iowait", color: "var(--s3)", values: [1, 1, 2] },
      { name: "steal", color: "var(--s4)", values: [0, 0, 0] },
    ],
    mem: [
      { name: "used", color: "var(--s1)", values: [3e9, 3.5e9, 4e9] },
      { name: "buffers", color: "var(--s2)", values: [1e8, 1e8, 1e8] },
      { name: "cached", color: "var(--s3)", values: [5e8, 5e8, 5e8] },
      { name: "arc", color: "var(--s4)", values: [2e8, 2e8, 2e8] },
    ],
    reporting: [10, 12, 11],
    memUsed: [3e9, 3.5e9, 4e9],
    rx: [1e6, 2e6, 1.5e6],
    tx: [5e5, 6e5, 4e5],
    net_rx_bytes: 1.5e6,
    net_tx_bytes: 4e5,
    fullest: { mount: "/data", pct: 88 },
    disk: [],
    ...overrides,
  };
}

// The location under the hostname, one string per line it renders on -- the
// country sits on a second .host-cell-site of its own, so asserting the first
// one alone would pass on a cell that had silently dropped it.
function siteLines(container: HTMLElement): string[] {
  return [...container.querySelectorAll(".host-cell-site")].map(
    (el) => el.textContent ?? "",
  );
}

// The Disk line is the one sparkline in this row scaled to its own window
// rather than to a fixed 0-100: five points of growth over a day is 1.3px
// inside a 26px strip, and growth is the whole reason the line is there. The
// bar under it keeps saying how full the mount actually is.
describe("diskAxis", () => {
  it("fits the axis to the window's own extent", () => {
    expect(diskAxis([66, 68, 71])).toEqual({ min: 66, max: 71 });
  });

  // Without a floor on the span, a mount that did not move all day draws its
  // own rounding as a mountain range.
  it("widens a near-flat mount to the minimum span", () => {
    expect(diskAxis([41.9, 42, 42.1])).toEqual({ min: 41, max: 43 });
  });

  it("widens a mount that did not move at all", () => {
    expect(diskAxis([50, 50, 50])).toEqual({ min: 49, max: 51 });
  });

  // Slid back inside the axis rather than clipped, so the same growth draws
  // the same steepness on a near-empty disk as on a near-full one.
  it("slides the widened span back inside 0-100 at either end", () => {
    expect(diskAxis([0.2, 0.4])).toEqual({ min: 0, max: 2 });
    expect(diskAxis([99.6, 99.8])).toEqual({ min: 98, max: 100 });
  });

  // Gaps are not readings, and a mount that reported nothing has no line.
  it("ignores gaps, and answers null when there is no reading at all", () => {
    expect(diskAxis([null, 60, null, 70])).toEqual({ min: 60, max: 70 });
    expect(diskAxis([null, null])).toBeNull();
    expect(diskAxis([])).toBeNull();
  });
});

describe("hostColumns", () => {
  // Uptime is gone: it is a fact about a host rather than a reading to scan
  // a fleet by -- the same number all day, where the row's job is what
  // changed. It still leads the host page's System card.
  // Filesystem is gone too, and deliberately: one filesystem column, not two.
  // Disk answers both halves in one cell -- how close to full the mount is
  // now, on its bar, and whether it is filling up, on the line above it. The
  // second column drew every mount on a fixed axis, which is a texture rather
  // than a reading; all of them together are one click away on the host page.
  // Traffic sits last. It is the one column with no bar and no threshold, so
  // it ran through the middle of the three that have both; the gauges are the
  // block a reader scans for what needs acting on, and they now run
  // uninterrupted.
  // Last seen sits at the right end, after Traffic: it is the row's quietest
  // fact, and beside the hostname it would have split the identity from the
  // block of gauges the list is scanned by -- see the column's own note.
  it("yields Host, CPU, Memory, Filesystem, Traffic, Last seen in that exact order", () => {
    const cols = hostColumns("1h");
    expect(cols.map((c) => c.header)).toEqual([
      "Host",
      "CPU",
      "Memory",
      "Filesystem",
      "Traffic",
      "Last seen",
    ]);
  });

  it("consumes the range parameter (build fails silently otherwise via noUnusedParameters)", () => {
    // Both calls must succeed and be independent column arrays.
    expect(hostColumns("1h")).toHaveLength(6);
    expect(hostColumns("24h")).toHaveLength(6);
  });

  // The accessors are separate from the cell on purpose -- a cell is a
  // sparkline or a meter and has no order -- so they need their own check
  // that they read the same fact the cell shows.
  describe("sorting", () => {
    it("sorts hosts on the name shown, and disks on the percentage shown", () => {
      const cols = hostColumns("1h");
      const row = makeRow({
        hostname: "web-01",
        fullest: { mount: "/data", pct: 88 },
      });

      const host = cols.find((c) => c.header === "Host")!;
      const disk = cols.find((c) => c.header === "Filesystem")!;
      expect(host.sortValue!(row)).toBe("web-01");
      // The percentage, not bytes: sorting on size would put the biggest
      // disk first rather than the one closest to filling up.
      expect(disk.sortValue!(row)).toBe(88);
    });

    // Unknown must not sort as zero, or a host whose filesystems were never
    // read would rank as the emptiest disk on the page.
    it("gives a host with no filesystems no disk sort value at all", () => {
      const disk = hostColumns("1h").find((c) => c.header === "Filesystem")!;

      expect(disk.sortValue!(makeRow({ fullest: null }))).toBeNull();
    });

    // The chart columns sort too. They did not, on the argument that a
    // sparkline has no order -- true of the picture, and not of the reading
    // it draws: the right-hand edge is a number, it is the number the reader
    // takes off the cell, and four of six columns offering no control at all
    // read as a broken table rather than as a considered one.
    it("orders every column, charts included", () => {
      const cols = hostColumns("1h");

      for (const col of cols) {
        expect(col.sortValue, `${col.header} has no sortValue`).toBeDefined();
      }
    });

    // cpu_total's last bucket -- where the silhouette the cell draws ends --
    // and never the per-core bands, which are the enlarged view's and sum to
    // something else here on purpose.
    it("sorts CPU on where the cpu_total silhouette ends", () => {
      const cpu = hostColumns("1h").find((c) => c.header === "CPU")!;

      // Live, because a stale host sorts as unknown whatever its series says.
      expect(
        cpu.sortValue!(
          makeRow({
            last_seen: new Date().toISOString(),
            reporting: [10, 12, 11],
            cpu: [{ name: "core 0", color: "var(--cpu-1)", values: [90, 90] }],
          }),
        ),
      ).toBe(11);
    });

    // The last REPORTED value, not the last bucket: the newest bucket is null
    // whenever the rollup has not landed yet, and a host must not drop to the
    // bottom of the list every time that happens.
    it("reads cpu_total's last reported value past a trailing gap", () => {
      const cpu = hostColumns("1h").find((c) => c.header === "CPU")!;
      const row = makeRow({
        last_seen: new Date().toISOString(),
        reporting: [10, 18, null],
      });

      expect(cpu.sortValue!(row)).toBe(18);
    });

    it("gives a host that has never reported CPU no sort value", () => {
      const cpu = hostColumns("1h").find((c) => c.header === "CPU")!;

      expect(cpu.sortValue!(makeRow({ reporting: [] }))).toBeNull();
    });

    // A fraction of the ceiling the cell draws against, never the bytes: on
    // bytes the fleet would order by how much RAM each machine HAS.
    // The gauge the cell prints, not the height of the stack beside it: the
    // bands here sum to 15 of 16 GB because the kernel is caching, and
    // ordering on that ranks the fleet by how little memory is FREE. This row
    // is using 4 GB and sorts as a quarter full, which is what its cell says.
    it("sorts Memory on the fraction of mem_total in use", () => {
      const memory = hostColumns("1h").find((c) => c.header === "Memory")!;
      const row = makeRow({
        mem_total: 16_000_000_000,
        mem_used: 4_000_000_000,
        last_seen: new Date().toISOString(),
        mem: [
          { name: "used", color: "var(--s1)", values: [null, 4e9] },
          { name: "cached", color: "var(--s3)", values: [null, 11e9] },
        ],
      });

      expect(memory.sortValue!(row)).toBeCloseTo(0.25);
    });

    // A host that has posted metrics but no memory gauge yet sorts as
    // unknown rather than as empty -- the same rule the cell follows when it
    // prints nothing at all.
    it("gives a host with no memory reading no sort value", () => {
      const memory = hostColumns("1h").find((c) => c.header === "Memory")!;

      expect(
        memory.sortValue!(
          makeRow({
            mem_used: null,
            mem_total: 16_000_000_000,
            last_seen: new Date().toISOString(),
          }),
        ),
      ).toBeNull();
    });

    it("gives a host with no memory ceiling no sort value", () => {
      const memory = hostColumns("1h").find((c) => c.header === "Memory")!;

      expect(memory.sortValue!(makeRow({ mem_total: null }))).toBeNull();
    });

    // Both directions summed: the cell prints in AND out, and the fleet
    // question is which host is moving the most.
    it("sorts Traffic on the two rates the cell prints", () => {
      const traffic = hostColumns("1h").find((c) => c.header === "Traffic")!;
      const row = makeRow({
        last_seen: new Date().toISOString(),
        net_rx_bytes: 1.5e6,
        net_tx_bytes: 4e5,
      });

      expect(traffic.sortValue!(row)).toBe(1.9e6);
    });

    // The cell prints nothing for a host that has gone quiet, and a list
    // ordered by a number it refuses to draw would put that host at the top.
    it("gives a host that is no longer reporting no traffic sort value", () => {
      const traffic = hostColumns("1h").find((c) => c.header === "Traffic")!;
      const row = makeRow({ last_seen: "2020-01-01T00:00:00Z" });

      expect(traffic.sortValue!(row)).toBeNull();
    });

    // The percentage cannot say on its own whether a mount is in trouble:
    // 90% of a 6.7 TB array is 674 GB free and 90% of a 4 GB root is not.
    it("prints the mount, how full it is, and what is left", () => {
      const disk = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const { container } = render(
        <>
          {disk.cell(
            makeRow({
              fullest: {
                mount: "/var/log",
                pct: 91,
                free: 3_100_000_000,
              },
            }),
          )}
        </>,
      );

      expect(container.querySelector(".dmount")?.textContent).toBe("/var/log");
      expect(
        container.querySelector(".disk-cell .metric-now .v")?.textContent,
      ).toBe("91%");
      expect(container.querySelector(".dfree")?.textContent).toBe(
        "3.1 GB left",
      );
    });

    // The bar's fill and the figure beside it are the same reading, so they
    // take the same severity -- a red bar over an ink number said one thing
    // twice and only half of it the second time.
    it("colours the percentage with the fill's own severity", () => {
      const disk = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const crit = render(
        <>{disk.cell(makeRow({ fullest: { mount: "/", pct: 96 } }))}</>,
      );
      expect(
        crit.container.querySelector(".disk-cell .metric-now .v.st-crit"),
      ).toBeInTheDocument();
      crit.unmount();

      const calm = render(
        <>{disk.cell(makeRow({ fullest: { mount: "/", pct: 21 } }))}</>,
      );
      // Ok is a severity like any other and it is drawn: green says netra
      // measured this mount and it is fine, where plain ink said the same
      // thing an empty cell does.
      expect(
        calm.container.querySelector(".disk-cell .metric-now .v")?.className,
      ).toBe("v st-ok");
    });

    // free is optional on the row -- the assembler leaves it unset when the
    // host reported no size -- and an absent fact prints nothing, never a
    // dash. Same rule the readings and the location line follow.
    it("says nothing about free space when the host did not report it", () => {
      const disk = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const { container } = render(
        <>{disk.cell(makeRow({ fullest: { mount: "/", pct: 40 } }))}</>,
      );

      expect(container.querySelector(".dfree")).toBeNull();
    });
  });

  describe("host cell", () => {
    // The name is plain ink on every row and the status hue on the rows that
    // have stopped answering -- accent on all of them marked nothing, since
    // every row has a name. Critical only: a sporadic host IS answering, and
    // its amber badge is where that belongs.
    it("marks a hostname that is no longer reporting, and no other", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const name = (row: HostRow): Element => {
        const { container } = render(<>{col.cell(row)}</>);
        return container.querySelector(".host-cell-name")!;
      };

      expect(
        name(makeRow({ last_seen: "2020-01-01T00:00:00Z" })).className,
      ).toContain("gone");
      expect(
        name(makeRow({ last_seen: new Date().toISOString() })).className,
      ).not.toContain("gone");
    });

    // The mark is the whole point of the column's second element, and what it
    // has to get right is the RANKING -- see hostMark. Every case below is a
    // host that is more than one thing at once.
    describe("severity mark", () => {
      const now = () => new Date().toISOString();
      const mark = (
        row: HostRow,
        worst?: (row: HostRow) => "warning" | "critical" | null,
        sporadic?: (row: HostRow) => boolean,
      ): Element | null => {
        const col = hostColumns("1h", worst, sporadic).find(
          (c) => c.header === "Host",
        )!;
        const { container } = render(<>{col.cell(row)}</>);
        return container.querySelector(".smark");
      };

      it("says nothing about a host with nothing wrong", () => {
        expect(mark(makeRow({ last_seen: now() }), () => null)).toBeNull();
      });

      // A host nobody has heard from marks as critical and draws critical's
      // mark -- the same glyph, the same hue. What a reader takes off this
      // column is how bad, not which kind; the kind is in the rest of the row
      // (every figure absent on a host that is gone) and in the mark's own
      // accessible name. It drew nothing at all for one commit, on the
      // argument that the red name and Last seen say it twice already -- they
      // do, and it was still wrong: severity may not ride on colour alone and
      // an age is not a severity.
      it("marks a silent host as critical, and names it offline", () => {
        const el = mark(makeRow({ last_seen: "2020-01-01T00:00:00Z" }));
        expect(el).toHaveClass("st-crit");
        expect(el).toHaveAttribute("aria-label", "offline");
      });

      // The glyph is shared, so the WORD is the only thing separating a host
      // that is gone from one that is merely in trouble. If that ever stops
      // being the row's own word, the two become the same to a screen reader.
      it("draws one glyph for both, separated only by the word", () => {
        const now = new Date().toISOString();
        const gone = mark(makeRow({ last_seen: "2020-01-01T00:00:00Z" }))!;
        const live = mark(makeRow({ last_seen: now }), () => "critical")!;

        expect(gone.getAttribute("class")).toBe(live.getAttribute("class"));
        expect(gone.getAttribute("aria-label")).not.toBe(
          live.getAttribute("aria-label"),
        );
      });

      // The word is the row's own, not the severity band it falls in -- it is
      // the whole of what a screen reader gets in place of the mark.
      it("names a host that has never reported by its own word", () => {
        expect(mark(makeRow({ last_seen: null }))).toHaveAttribute(
          "aria-label",
          "never seen",
        );
      });

      it("marks a reporting host's worst condition at its severity", () => {
        expect(
          mark(makeRow({ last_seen: now() }), () => "critical"),
        ).toHaveClass("st-crit");
        expect(
          mark(makeRow({ last_seen: now() }), () => "warning"),
        ).toHaveClass("st-warn");
      });

      // The rule the fleet is read by: a machine nobody has heard from has
      // stale figures for everything else, so a "critical" derived from its
      // last known disk reading would claim to describe this minute. Offline
      // outranks it, and says so with its own mark rather than borrowing the
      // octagon. The disk that WAS 96% full is in the row's own Filesystem
      // cell either way.
      it("says offline, not critical, for a silent host that also has one", () => {
        expect(
          mark(
            makeRow({ last_seen: "2020-01-01T00:00:00Z" }),
            () => "critical",
          ),
        ).toHaveAttribute("aria-label", "offline");
      });

      // The other direction, and it is NOT symmetric: a sporadic host is
      // answering, so a critical reading off it is current and outranks the
      // gaps in its series.
      //
      // Sporadic is the HUB's verdict now, read off the same conditions the
      // severity is, so the mark's shape and its colour cannot come from two
      // different answers.
      it("lets a critical outrank sporadic, and sporadic hold at warning", () => {
        const gappy = makeRow({ last_seen: now() });

        expect(
          mark(
            gappy,
            () => "critical",
            () => true,
          ),
        ).toHaveClass("st-crit");
        expect(
          mark(
            gappy,
            () => null,
            () => true,
          ),
        ).toHaveClass("st-warn");
      });

      // One mark, never two -- the ranking is this column's to do, not the
      // reader's.
      it("draws exactly one mark however many things are wrong", () => {
        const col = hostColumns(
          "1h",
          () => "critical",
          () => true,
        ).find((c) => c.header === "Host")!;
        const { container } = render(
          <>{col.cell(makeRow({ last_seen: now() }))}</>,
        );

        expect(container.querySelectorAll(".smark")).toHaveLength(1);
      });
    });

    // The provider and the place, both reported by the host's own agent. The
    // fleet used to print the site NAME here -- an internal label out of a
    // table somebody fills in by hand, which told a reader scanning the list
    // nothing the hostname beside it had not already said.
    it("writes the provider and the location under the hostname", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(
        <>
          {col.cell(makeRow({ provider: "OVH", location: "Roubaix, France" }))}
        </>,
      );

      expect(siteLines(container)).toEqual(["OVH · Roubaix", "France"]);
    });

    // The country under the place rather than beside it: on one line the
    // location was the widest text in the row and ellipsised away on exactly
    // the fleets that had bothered to report a country. The comma goes with
    // the break -- it separated two things that are no longer on one line.
    it("breaks the country onto its own line, at the last comma", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const lines = (location: string) =>
        siteLines(
          render(<>{col.cell(makeRow({ provider: null, location }))}</>)
            .container,
        );

      expect(lines("Roubaix, France")).toEqual(["Roubaix", "France"]);
      expect(lines("Roubaix, Hauts-de-France, France")).toEqual([
        "Roubaix, Hauts-de-France",
        "France",
      ]);
    });

    // The place exactly as the agent sent it otherwise. AGENT_LOCATION is
    // free text an operator wrote, so anything this did to that string beyond
    // the break -- resolving a code, changing the case -- would be this UI
    // overruling the person who typed it. A string with no comma to break at,
    // or one whose comma has nothing after it, stays as it came.
    it("prints the reported location verbatim", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const lines = (location: string) =>
        siteLines(
          render(<>{col.cell(makeRow({ provider: null, location }))}</>)
            .container,
        );

      expect(lines("basement")).toEqual(["basement"]);
      expect(lines("AWS eu-west-1a")).toEqual(["AWS eu-west-1a"]);
      expect(lines("Roubaix,")).toEqual(["Roubaix,"]);
      expect(lines(", France")).toEqual([", France"]);
      // Whitespace is not a first line: this one has a comma past index 0 and
      // a country after it, and splitting it would leave the cell opening on
      // a blank row.
      expect(lines(" , France")).toEqual([" , France"]);
    });

    // Each half stands on its own: an operator who set only one of the two
    // variables gets the one they set, not a separator with a gap beside it.
    it("leaves out the half the agent did not report", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const cell = (over: Partial<HostRow>) =>
        siteLines(render(<>{col.cell(makeRow(over))}</>).container);

      expect(cell({ provider: null, location: "Roubaix, France" })).toEqual([
        "Roubaix",
        "France",
      ]);
      expect(cell({ provider: "OVH", location: null })).toEqual(["OVH"]);
    });

    // Setting neither variable is the common case, and it must write no line
    // rather than an em dash: a column of dashes under every hostname reads as
    // a fleet full of holes, where nothing at all reads as nothing to say.
    it("writes no line at all when the agent reported no location", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(
        <>{col.cell(makeRow({ provider: null, location: null }))}</>,
      );

      expect(container.querySelector(".host-cell-site")).toBeNull();
      expect(container.textContent).not.toContain(ABSENT);
    });
  });

  describe("disk cell", () => {
    // The cell reports the ONE mount worth acting on, so the line under it
    // names that mount and nothing else. It used to carry a "+N" count of the
    // host's other filesystems, which answered a question the cell does not
    // ask -- none of those mounts is what the bar, the figure or the trend
    // above it is about.
    it("names the fullest mount, with no count of the others", () => {
      const cols = hostColumns("1h");
      const diskCol = cols.find((c) => c.header === "Filesystem")!;
      const row = makeRow({ fullest: { mount: "/data", pct: 88 } });
      render(<>{diskCol.cell(row)}</>);
      expect(screen.getByText("/data")).toBeInTheDocument();
      expect(screen.queryByText(/\+\d/)).not.toBeInTheDocument();
    });

    // The line is about the mount the figure beside it names, so it is named
    // after that mount and not after the column: twenty rows of "Filesystem trend"
    // name twenty different charts identically.
    it("draws a trend line for the mount it names, and can be enlarged", () => {
      const diskCol = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const { container } = render(
        <>
          {diskCol.cell(
            makeRow({
              hostname: "db-02",
              fullest: {
                mount: "/var/lib/postgresql",
                pct: 71,
                series: [66, 68, 71],
              },
            }),
          )}
        </>,
      );

      expect(container.querySelector("svg.spark")).toBeInTheDocument();
      expect(
        screen.getByLabelText(
          "Filesystem trend for /var/lib/postgresql, last 1h",
        ),
      ).toBeInTheDocument();
      // Shaded, like every other sparkline in the app -- a filled mass whose
      // top edge is the trend, not a bare stroke. The axis is fitted, so the
      // fill closes at the bottom of the box and tracks the line rather than
      // flooding the cell.
      // data-area, not a fill selector: a point dot carries the same fill too,
      // so the colour selector would pass on a bare line with a marker on it.
      expect(container.querySelector("path[data-area]")).toBeInTheDocument();
      expect(
        screen.getByRole("button", {
          name: "Enlarge filesystem usage for /var/lib/postgresql on db-02",
        }),
      ).toBeInTheDocument();
    });

    // A mount with no bucket carrying both used and free has no line to
    // draw. Drawing an empty box would put a chart where "not measured"
    // belongs -- the same argument the cell makes about an empty green bar --
    // so the bar stands alone and keeps its reserved strip, which is what
    // holds the three bars in a row on one line.
    it("draws the bar alone, still aligned, when there is no series", () => {
      const diskCol = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const { container } = render(
        <>{diskCol.cell(makeRow({ fullest: { mount: "/", pct: 40 } }))}</>,
      );

      expect(container.querySelector("svg.spark")).toBeNull();
      expect(
        (container.querySelector(".disk-cell") as HTMLElement).style.paddingTop,
      ).not.toBe("");
      expect(screen.getByText("/")).toBeInTheDocument();
    });

    // The mark labels the host the way a device list does elsewhere: a reader
    // picks a Debian box out of a page of Ubuntu ones without reading a word.
    it("draws the distribution mark, and says which release under the name", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(
        <>
          {col.cell(
            makeRow({
              os_name: "Debian 13",
              provider: "Init7",
              location: "Winterthur, CH",
            }),
          )}
        </>,
      );

      expect(container.querySelector(".osicon")).toBeInTheDocument();
      // The line under the name says where the host is and nothing else: the
      // mark beside it already says which distribution, and spelling out
      // "Debian GNU/Linux 12 (bookworm)" made the row's longest string out of
      // something it had just drawn.
      expect(siteLines(container)).toEqual(["Init7 \u00b7 Winterthur", "CH"]);
    });

    // An OS with no mark of its own leaves the space empty rather than taking
    // a placeholder -- the same rule the missing location line follows.
    it("draws no mark for an OS it has none for", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(
        <>{col.cell(makeRow({ os_name: "SomeVendorOS 4" }))}</>,
      );

      expect(container.querySelector(".osicon")).toBeNull();
    });

    // A host that has posted metrics but no metadata yet: no mark, and the
    // line under the name is whatever the location was, or nothing at all.
    it("says only the location when the OS is unknown", () => {
      const col = hostColumns("1h").find((c) => c.header === "Host")!;
      const { container } = render(
        <>
          {col.cell(
            makeRow({ os_name: null, provider: null, location: "Zurich, CH" }),
          )}
        </>,
      );

      expect(container.querySelector(".osicon")).toBeNull();
      expect(siteLines(container)).toEqual(["Zurich", "CH"]);
    });
  });

  describe("cpu cell", () => {
    // Without a ceiling Sparkline auto-scales each host to its own extent,
    // so an idle host and a saturated one draw the identical silhouette and
    // the rows stop being comparable -- the one thing a fleet list is for.
    // Two rows an order of magnitude apart must not render the same path
    // data.
    it("scales every host's silhouette to 100, not to its own peak", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const idle = render(
        <>{cpuCol.cell(makeRow({ reporting: [1, 2, 3] }))}</>,
      );
      const idlePaths = idle.container.innerHTML;
      idle.unmount();

      const busy = render(
        <>{cpuCol.cell(makeRow({ reporting: [30, 60, 90] }))}</>,
      );

      expect(busy.container.innerHTML).not.toBe(idlePaths);
    });

    // The cell draws cpu_total as one silhouette, and never the per-core
    // stack: up to 32 bands in four cycling blues inside 45px was a texture
    // whose hues meant "core index", and it buried the one thing a fleet
    // glance reads off this cell -- whether the top edge moved. The stack is
    // what the enlarged view opens on, not what the row draws.
    it("draws one silhouette from cpu_total, not the per-core stack", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const row = makeRow({
        reporting: [10, 50, 90],
        cpu: Array.from({ length: 32 }, (_, i) => ({
          name: `core ${i}`,
          color: `var(--cpu-${(i % 4) + 1})`,
          values: [1, 2, 3],
        })),
      });
      const { container } = render(<>{cpuCol.cell(row)}</>);

      const series = container.querySelectorAll("[data-series]");
      expect(series).toHaveLength(1);
      expect(container.querySelector("path[data-band]")).toBeNull();
      // Both ends pinned, so the silhouette is a fraction of the box: a line
      // from the series' own minimum would put a host steady at 40% along
      // the bottom edge.
      const line = container.querySelector("path[data-line]")!;
      const expected = linePath(
        row.reporting,
        SPARK_WIDTH,
        SPARK_STRIP_HEIGHT,
        0,
        100,
        2,
      ).paths;
      expect(line.getAttribute("d")).toBe(expected[0]);
      // Filled to the baseline, in the row's neutral: this fixture's host
      // stopped reporting, so there is no current value to judge and the
      // silhouette is history without a reading. See the severity case below.
      const area = container.querySelector("path[data-area]")!;
      expect(area.getAttribute("fill")).toBe("var(--ink-2)");
    });

    // The whole cell is one reading: the silhouette takes the severity of the
    // value the bar under it prints, ok included. Grey is what this table
    // draws when it has nothing -- no data, not reporting -- so a healthy
    // host has to look different from an empty one.
    it("draws the silhouette in the severity of the value under it", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const fill = (pct: number) => {
        const { container, unmount } = render(
          <>
            {cpuCol.cell(
              makeRow({
                last_seen: new Date().toISOString(),
                reporting: [10, 20, pct],
              }),
            )}
          </>,
        );
        const got = container
          .querySelector("path[data-area]")!
          .getAttribute("fill");
        unmount();
        return got;
      };
      expect(fill(21)).toBe("var(--st-ok)");
      expect(fill(76)).toBe("var(--st-warn)");
      expect(fill(88)).toBe("var(--st-warn)");
      expect(fill(96)).toBe("var(--st-crit)");
    });

    // The cell drew a shape and no number: "how loaded is that host" was
    // answerable only by eye, and only against the row above it.
    it("prints where the silhouette ends and what it is a fraction of", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const { container } = render(
        <>
          {cpuCol.cell(
            makeRow({
              threads: 8,
              // Recent, like the traffic-rate tests above: the fixture's own
              // last_seen is fixed in the past, and a stale host prints no
              // reading at all.
              last_seen: new Date().toISOString(),
              // cpu_total, the series the cell draws -- never the per-core
              // bands, which sum to something else here on purpose.
              reporting: [10, 20, 34],
              cpu: [
                { name: "user", color: "var(--s1)", values: [10, 20, 30] },
                { name: "system", color: "var(--s2)", values: [1, 2, 4] },
              ],
            }),
          )}
        </>,
      );

      // The last bucket of cpu_total, the same figure the column sorts on.
      expect(container.textContent).toContain("34");
      expect(container.textContent).toContain("of 8 cores");
    });

    // The same guard the traffic rates two cells over apply. A confident
    // percentage beside a sparkline that has gone to a gap is the one reading
    // in this row that can actively mislead.
    it("prints no reading for a host that stopped reporting", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const { container } = render(
        <>
          {cpuCol.cell(
            makeRow({
              threads: 8,
              last_seen: "2020-01-01T00:00:00Z",
              cpu: [{ name: "user", color: "var(--s1)", values: [10, 20, 30] }],
            }),
          )}
        </>,
      );

      expect(container.querySelector(".metric-now")).toBeNull();
      // And not a dash standing in for it either.
      expect(container.textContent).not.toContain("\u2014");
    });

    // A single-thread host is a real fleet member, and "of 1 cores" is the
    // kind of line that makes a reader distrust every other figure on it.
    it("pluralises the core count", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const { container } = render(
        <>
          {cpuCol.cell(
            makeRow({ threads: 1, last_seen: new Date().toISOString() }),
          )}
        </>,
      );

      expect(container.textContent).toContain("of 1 core");
      expect(container.textContent).not.toContain("of 1 cores");
    });

    // threads is nullable -- a host that has posted metrics but no metadata
    // yet has none -- and a reading with nothing under it is better than one
    // under "of null cores".
    it("omits the unit line when the host has not reported its core count", () => {
      const cpuCol = hostColumns("1h").find((c) => c.header === "CPU")!;
      const { container } = render(
        <>
          {cpuCol.cell(
            makeRow({ threads: null, last_seen: new Date().toISOString() }),
          )}
        </>,
      );

      expect(container.querySelector(".metric-now .v")).toBeInTheDocument();
      expect(container.querySelector(".metric-cell .u")).toBeNull();
    });
  });

  describe("disk cell", () => {
    // A host that has reported no filesystems has no fullest one. The row
    // type used to forbid saying so, and the only expressible stand-in was
    // pct: 0 -- an empty, healthy, green bar where "never collected"
    // belongs, absent rendered as a fact.
    it("renders neither a meter nor a dash when nothing was collected", () => {
      const diskCol = hostColumns("1h").find((c) => c.header === "Filesystem")!;
      const { container } = render(
        <>{diskCol.cell(makeRow({ fullest: null }))}</>,
      );

      expect(container.querySelector(".segbar")).not.toBeInTheDocument();
      expect(container.textContent).toBe("");
    });

    // The bug. A host switched off overnight used to hit the branch above --
    // its reading came off the last slot of the answered window, so there was
    // no fullest mount and the whole cell went, chart included. CPU and
    // Memory beside it keep their history and drop only their now-bar; disk
    // fullness does not change while a machine is off, so the reading stays
    // too. It comes from the hub's stored gauge now, which no window can
    // empty.
    it("keeps the bar, the figure and the line on a host that is switched off", () => {
      const diskCol = hostColumns("24h").find(
        (c) => c.header === "Filesystem",
      )!;
      const { container } = render(
        <>
          {diskCol.cell(
            makeRow({
              // Three days quiet.
              last_seen: new Date(Date.now() - 3 * 86_400_000).toISOString(),
              fullest: {
                mount: "/srv/pool",
                pct: 87,
                free: 1_400_000_000_000,
                // The shape up to the moment it stopped, gap kept.
                series: [80, 84, null, null],
                asOf: new Date(Date.now() - 3 * 86_400_000).toISOString(),
              },
            }),
          )}
        </>,
      );

      // Full weight, exactly as a live host draws it: the figure is still
      // true, and the row's status chip and its "stopped reporting" condition
      // are what say how long ago it was measured.
      expect(container.querySelector(".segbar")).toBeInTheDocument();
      expect(container.querySelector(".metric-now .v")?.textContent).toBe(
        "87%",
      );
      expect(container.textContent).toContain("/srv/pool");
      expect(container.querySelector("svg.spark")).toBeInTheDocument();
    });

    // The weekend case: off for longer than the 24 h window is wide, so there
    // is no history to draw. The reading survives regardless, because it does
    // not come from the window.
    it("draws the reading with no line when the window holds nothing", () => {
      const diskCol = hostColumns("24h").find(
        (c) => c.header === "Filesystem",
      )!;
      const { container } = render(
        <>
          {diskCol.cell(
            makeRow({
              last_seen: new Date(Date.now() - 3 * 86_400_000).toISOString(),
              fullest: {
                mount: "/srv/pool",
                pct: 87,
                free: 1_400_000_000_000,
                series: [],
              },
            }),
          )}
        </>,
      );

      expect(container.querySelector(".segbar")).toBeInTheDocument();
      expect(container.querySelector("svg.spark")).not.toBeInTheDocument();
    });
  });

  describe("traffic cell", () => {
    it("renders rx and tx in identical typographic weight, distinguished only by direction", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      // last_seen must be RECENT: a host that stopped reporting now prints no
      // rates at all (see the blanking test below), so a stale fixture would
      // pass this by rendering nothing rather than by rendering two equals.
      const row = makeRow({
        rx: [1e6, 2e6],
        tx: [5e5, 6e5],
        last_seen: new Date().toISOString(),
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);
      const rates = container.querySelectorAll(".rate");
      expect(rates).toHaveLength(2);
      const [rxEl, txEl] = Array.from(rates);
      // Same class, no per-element inline font styling -- neither rate is
      // asserted as more important than the other.
      expect(rxEl!.className).toBe(txEl!.className);
      expect(rxEl!.getAttribute("style")).toBeNull();
      expect(txEl!.getAttribute("style")).toBeNull();
      // The only distinguishing signal is the arrow, and each carries its
      // own accessible label so the direction survives without colour.
      expect(rxEl!.getAttribute("aria-label")).toMatch(/inbound/i);
      expect(txEl!.getAttribute("aria-label")).toMatch(/outbound/i);
    });

    // rx_bytes and tx_bytes are BYTES per second -- network.go divides a
    // byte delta by the elapsed seconds. Rendered through bitrate() they
    // read 8x low and entirely plausible: 1 MB/s showed as "1 Mb/s". The
    // host overview's Traffic card had the identical bug, so the two pages
    // agreed with each other and with nothing else.
    it("renders traffic in bytes per second, not bits", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      // Reporting now: the cell blanks the rates of a host that has gone
      // quiet, and the row fixture's last_seen is fixed in the past.
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        net_rx_bytes: 2e6,
        net_tx_bytes: 1e6,
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);

      expect(container.textContent).toContain("2 MB/s");
      expect(container.textContent).toContain("1 MB/s");
      expect(container.textContent).not.toMatch(/b\/s/);
    });

    // The reported bug. The rates are host_current's gauges; only the
    // sparkline comes from the series. Read off the series they moved with
    // the RANGE -- the raw instantaneous rate at 1h, a five-minute average
    // from a quarter of an hour ago at 6h and wider -- so the number beside
    // a chart changed when the chart was widened. The two disagree here
    // precisely so that reading the wrong one fails.
    it("takes its rates from the gauge, not the end of the series", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
        net_rx_bytes: 2e6,
        net_tx_bytes: 1e6,
        rx: [1e6, 9e6],
        tx: [5e5, 9e6],
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);

      expect(container.textContent).toContain("2 MB/s");
      expect(container.textContent).toContain("1 MB/s");
      expect(container.textContent).not.toContain("9 MB/s");
    });

    // The gauge is the one number on this row that does not go absent on its
    // own when the agent dies: host_current keeps the last pair it was
    // written, and the upsert coalesces so a post carrying no net samples
    // cannot clear it either. Ungated, the cell drew a steady rate beside
    // this row's own "offline" badge -- "the agent is down" rendered as
    // "traffic is steady", which is exactly what the series version's
    // trailing-null check used to prevent.
    it("blanks the rates of a host that stopped reporting", () => {
      const cols = hostColumns("1h");
      const trafficCol = cols.find((c) => c.header === "Traffic")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 600_000).toISOString(),
        net_rx_bytes: 2e6,
        net_tx_bytes: 1e6,
      });
      const { container } = render(<>{trafficCol.cell(row)}</>);

      expect(container.textContent).not.toContain("MB/s");
      // Nothing at all, not a dash: stacked two deep beside a chart, "↑ – ↓ –"
      // reads as a pair of readings rather than as their absence. The gap in
      // the sparkline says the host stopped talking. ABSENT, not a literal:
      // a hardcoded dash stops matching the marker the moment it changes.
      expect(container.textContent).not.toContain(ABSENT);
      expect(container.querySelectorAll(".rate")).toHaveLength(0);
    });
  });

  describe("memory cell", () => {
    // Removing the sparkline legend was aimed at the 32-core CPU cell, where
    // a 32-entry list was taller than the row and thirty-two cores have no
    // identity a legend could carry anyway. It also stripped this cell,
    // where five separately-coloured bands were left carrying their identity
    // on colour alone -- which is the one thing a legend exists to prevent.
    it("carries no band legend, like every other sparkline in the row", () => {
      const cols = hostColumns("1h");
      const memCol = cols.find((c) => c.header === "Memory")!;
      const row = makeRow({
        mem_total: 16_000_000_000,
        mem: [
          { name: "used", color: "var(--s1)", values: [3e9, 3e9] },
          { name: "ARC", color: "var(--s7)", values: [2e8, 2e8] },
          { name: "buffers", color: "var(--s2)", values: [1e8, 1e8] },
          { name: "cached", color: "var(--s8)", values: [5e8, 5e8] },
          { name: "shared", color: "var(--s4)", values: [5e7, 5e7] },
        ],
      });

      const { container } = render(<>{memCol.cell(row)}</>);

      // The shape is the message in a dense list; naming five bands under a
      // 45px chart costs more row height than the names are worth, and the
      // host page's Memory panel is where the breakdown gets named. A
      // previous review turned this back on for the memory cell alone.
      expect(container.querySelector(".legend")).toBeNull();
      for (const name of ["used", "ARC", "buffers", "cached", "shared"]) {
        expect(container.textContent).not.toContain(name);
      }
    });

    // The caller that motivated removing it stays legend-free: thirty-two
    // hairlines cannot each own a hue, and the list was five times taller
    // than the chart it explained.
    it("leaves the per-core CPU cell without one", () => {
      const cols = hostColumns("1h");
      const cpuCol = cols.find((c) => c.header === "CPU")!;
      const row = makeRow({
        cpu: Array.from({ length: 32 }, (_, i) => ({
          name: `core ${i}`,
          color: `hsl(${i * 9} 60% 50%)`,
          values: [1, 2],
        })),
      });

      const { container } = render(<>{cpuCol.cell(row)}</>);

      expect(container.querySelector(".legend")).toBeNull();
    });

    // mem_used as one silhouette, scaled against mem_total -- never the
    // five-band stack. The stack's top edge is "not free", which on a Linux
    // host that caches everything is nearly the whole box, so every row was
    // a near-full brick beside a figure saying 30%. The silhouette is the
    // quantity the figure is a gauge of, so the two are one reading.
    it("draws mem_used against mem_total, not the not-free stack, so the shape agrees with the figure", () => {
      const cols = hostColumns("1h");
      const memCol = cols.find((c) => c.header === "Memory")!;
      const row = makeRow({
        mem_total: 16_000_000_000,
        memUsed: [3e9, 4e9],
        // A stack that would fill the box if the cell drew it.
        mem: [
          { name: "used", color: "var(--s1)", values: [3e9, 3e9] },
          { name: "buffers", color: "var(--s2)", values: [1e9, 1e9] },
          { name: "cached", color: "var(--s3)", values: [9e9, 9e9] },
          { name: "arc", color: "var(--s4)", values: [2e9, 2e9] },
        ],
      });
      const { container } = render(<>{memCol.cell(row)}</>);
      // No dashed total rule in the row: the silhouette is scaled to
      // mem_total, so its height already says how full the host is, and the
      // enlarged chart is where the ceiling gets drawn as a rule.
      expect(container.querySelector("line[data-reference]")).toBeNull();
      expect(container.querySelector("path[data-band]")).toBeNull();
      expect(container.querySelectorAll("[data-series]")).toHaveLength(1);
      const line = container.querySelector("path[data-line]")!;
      const expected = linePath(
        row.memUsed!,
        // The shared sparkline size, not literals: every list chart reads it
        // from one pair of constants so a row's cells stay the same shape.
        SPARK_WIDTH,
        SPARK_STRIP_HEIGHT,
        // From zero to mem_total plus the headroom that keeps a full host off
        // the border. Free is the gap between the line and the top.
        0,
        (row.mem_total as number) * 1.08,
        2,
      ).paths;
      expect(line.getAttribute("d")).toBe(expected[0]);
      const area = container.querySelector("path[data-area]")!;
      expect(area.getAttribute("d")).toBe(
        areaPath(expected, SPARK_WIDTH, SPARK_STRIP_HEIGHT, 2)[0],
      );
      // The row's neutral, not a severity: this fixture stopped reporting, so
      // mem_used has no current value to judge. A reporting host draws the
      // same silhouette in its own severity -- see the CPU cell's case.
      expect(area.getAttribute("fill")).toBe("var(--ink-2)");
    });

    it("renders nothing at all, not a chart with an invented ceiling, when mem_total is unknown", () => {
      const cols = hostColumns("1h");
      const memCol = cols.find((c) => c.header === "Memory")!;
      const row = makeRow({ mem_total: null });
      const { container } = render(<>{memCol.cell(row)}</>);

      expect(container.querySelector("svg")).toBeNull();
      // Not a dash either: a column of em dashes down a silent host reads as
      // a reading, and the host cell's badge already explains the empty row.
      expect(container.textContent).toBe("");
    });

    // mem_used over mem_total, which is what `free` calls used and what the
    // host page prints for the same machine. NOT the height of the stack
    // beside it: that is everything which is not free, so the bands here sum
    // to 12 GB while the host is actually using 4.
    it("prints how full the host is, and what it is full of", () => {
      const memCol = hostColumns("1h").find((c) => c.header === "Memory")!;
      const { container } = render(
        <>
          {memCol.cell(
            makeRow({
              mem_total: 16_000_000_000,
              mem_used: 4_000_000_000,
              last_seen: new Date().toISOString(),
              mem: [
                { name: "used", color: "var(--s1)", values: [4e9, 4e9, 4e9] },
                { name: "cached", color: "var(--s3)", values: [8e9, 8e9, 8e9] },
              ],
            }),
          )}
        </>,
      );

      expect(container.querySelector(".metric-now .v")?.textContent).toBe(
        "25%",
      );
      // binaryBytes, like the enlarged chart's axis: 16 GB is 14.9 GiB.
      expect(container.textContent).toContain("of 14.9 GiB");
    });

    // The page cache is not memory a host has spent -- the kernel gives it
    // back on demand -- so a file server sitting at 97% "not free" is not a
    // host in trouble. This is the reading that made the fleet row disagree
    // with the host page about the same machine.
    it("does not count reclaimable cache as memory in use", () => {
      const memCol = hostColumns("1h").find((c) => c.header === "Memory")!;
      const { container } = render(
        <>
          {memCol.cell(
            makeRow({
              mem_total: 100,
              mem_used: 30,
              last_seen: new Date().toISOString(),
              mem: [
                { name: "used", color: "var(--s1)", values: [30] },
                { name: "cached", color: "var(--s3)", values: [67] },
              ],
            }),
          )}
        </>,
      );

      expect(container.querySelector(".metric-now .v")?.textContent).toBe(
        "30%",
      );
    });

    it("prints no reading for a host that stopped reporting", () => {
      const memCol = hostColumns("1h").find((c) => c.header === "Memory")!;
      const { container } = render(
        <>
          {memCol.cell(
            makeRow({
              last_seen: "2020-01-01T00:00:00Z",
              mem_total: 16_000_000_000,
            }),
          )}
        </>,
      );

      expect(container.querySelector(".metric-now")).toBeNull();
    });
  });

  describe("host cell", () => {
    // Severity never rides on colour alone (spec §3.3, and the header of
    // Badge.tsx on why: amber and crit measure ΔE 2.2 under deuteranopia).
    // The fleet mark answers it with SHAPE rather than with a word, so what
    // this pins is that the two severities are two different glyphs -- if a
    // future edit collapses them to one path in two colours, the mark stops
    // being a mitigation and this fails.
    it("draws a different glyph per severity, never one glyph in two colours", () => {
      const path = (worst: "warning" | "critical"): string => {
        const hostCol = hostColumns("1h", () => worst).find(
          (c) => c.header === "Host",
        )!;
        const { container } = render(
          <>{hostCol.cell(makeRow({ last_seen: new Date().toISOString() }))}</>,
        );
        return container.querySelector(".smark path")!.getAttribute("d")!;
      };

      expect(path("warning")).not.toBe(path("critical"));
    });

    // And the word is still there for anyone not reading the row by eye.
    it("names the severity for a screen reader", () => {
      const hostCol = hostColumns("1h", () => "critical").find(
        (c) => c.header === "Host",
      )!;
      render(
        <>{hostCol.cell(makeRow({ last_seen: new Date().toISOString() }))}</>,
      );
      expect(screen.getByLabelText("critical")).toBeInTheDocument();
      expect(screen.getByText("web-01")).toBeInTheDocument();
    });

    // The staleness threshold mirrors the product's own definition of
    // "down" (design spec: no POST within 3x the 60s scrape interval,
    // i.e. 180s) rather than a separately-invented number -- these two
    // tests pin the boundary at that 180s line, not at some other value
    // a future edit might drift to.
    // Healthy is the majority state, so it carries no mark at all: a row that
    // says "online" down the whole page spends the eye's first stop on the
    // word that never changes. The absence of a mark IS the healthy reading.
    //
    // What the boundary is read off is now the NAME rather than a badge --
    // the row's mark for a host that has gone quiet is .host-cell-name.gone,
    // and the pair of tests around this line is still what pins it at 180s.
    it("says nothing at 179s since last_seen, just under 3x the scrape interval", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const lastSeen = new Date(Date.now() - 179_000).toISOString();
      const row = makeRow({ last_seen: lastSeen });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(container.querySelector(".host-cell-name.gone")).toBeNull();
      expect(screen.queryByText("online")).toBeNull();
      expect(container.querySelector(".smark")).toBeNull();
    });

    // Answering now, but a fifth of the window missing. "online" and
    // "offline" are both wrong summaries of that host: one says it is fine,
    // the other says it is gone, and the interesting state is neither.
    //
    // The counting moved to the hub (conditions.SporadicSeverity), over a
    // FIXED window rather than the range the reader had picked -- the guards
    // it needs are pinned in internal/hub/conditions/rules_test.go and the
    // SQL that trims the window's edges in
    // TestIntegrationScanFindsASporadicHost. What is left here is the mark --
    // amber, the same one a warning condition draws. The word it used to
    // carry is gone with every other word in this column; what says it now is
    // the Last seen column ticking past a scrape interval while the row keeps
    // drawing figures.
    it("marks a host the hub called sporadic", () => {
      const cols = hostColumns("1h", undefined, () => true);
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
      });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(container.querySelector(".smark")).toHaveClass("st-warn");
    });

    // And the converse, so the test above cannot pass by the mark simply
    // always appearing.
    it("says nothing about a host the hub did not call sporadic", () => {
      const cols = hostColumns("1h", undefined, () => false);
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({
        last_seen: new Date(Date.now() - 10_000).toISOString(),
      });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(container.querySelector(".smark")).toBeNull();
    });

    // A host that has genuinely stopped is not sporadic, and it does not draw
    // sporadic's amber mark: it is offline, at the critical hue, and the gaps
    // in its series are that same outage said a second time.
    it("says offline rather than sporadic for a host that stopped", () => {
      const cols = hostColumns("1h", undefined, () => true);
      const hostCol = cols.find((c) => c.header === "Host")!;
      const row = makeRow({ last_seen: "2020-01-01T00:00:00Z" });
      const { container } = render(<>{hostCol.cell(row)}</>);
      const smark = container.querySelector(".smark")!;
      expect(smark).toHaveAttribute("aria-label", "offline");
      expect(smark).not.toHaveClass("st-warn");
      expect(
        container.querySelector(".host-cell-name.gone"),
      ).toBeInTheDocument();
    });

    it("reads offline at 181s since last_seen, just past 3x the scrape interval", () => {
      const cols = hostColumns("1h");
      const hostCol = cols.find((c) => c.header === "Host")!;
      const lastSeen = new Date(Date.now() - 181_000).toISOString();
      const row = makeRow({ last_seen: lastSeen });
      const { container } = render(<>{hostCol.cell(row)}</>);
      expect(
        container.querySelector(".host-cell-name.gone"),
      ).toBeInTheDocument();
    });
  });

  // The column the "offline" word was traded for. What it has to get right is
  // that it answers "since when", which is the question the word could not.
  describe("last seen", () => {
    const seen = (row: HostRow, now?: Date): Element => {
      const col = hostColumns("1h", undefined, undefined, now).find(
        (c) => c.header === "Last seen",
      )!;
      const { container } = render(<>{col.cell(row)}</>);
      return container.querySelector(".seen-cell")!;
    };

    it("prints an age against the page's clock, not the wall clock", () => {
      const now = new Date("2026-08-10T14:00:00Z");
      expect(
        seen(makeRow({ last_seen: "2026-08-10T11:46:00Z" }), now).textContent,
      ).toBe("2 h 14 m ago");
    });

    // A minute past the threshold and four days dead drew the identical
    // "offline" chip, and the difference between those two is the whole of
    // what a reader wants at that moment.
    it("separates a host just over the line from one long dead", () => {
      const now = new Date("2026-08-10T14:00:00Z");
      const justOver = seen(
        makeRow({ last_seen: "2026-08-10T13:56:00Z" }),
        now,
      ).textContent;
      const longDead = seen(
        makeRow({ last_seen: "2026-08-06T14:00:00Z" }),
        now,
      ).textContent;
      expect(justOver).toBe("4 m ago");
      expect(longDead).toBe("4 d ago");
    });

    // The WORD, not the absent dash. Four other cells on that row print the
    // dash for the ordinary reason that there is nothing to draw, so a fifth
    // one reads as "no value here" rather than as the host's condition -- and
    // on a never-seen row it is the only thing besides the name's hue that
    // states that condition at all. Severity may not ride on colour alone.
    it("says never, not a dash, for a host that has never reported", () => {
      const cell = seen(makeRow({ last_seen: null }));
      expect(cell.textContent).toBe("never");
      expect(cell.textContent).not.toBe(ABSENT);
    });

    // The exact instant belongs under the pointer -- an age is the scanning
    // reading, a timestamp is the one you take to a log.
    it("carries the exact instant on the title", () => {
      const cell = seen(makeRow({ last_seen: "2026-08-10T11:46:00Z" }));
      expect(cell.querySelector("[title]")!.getAttribute("title")).toBe(
        absolute("2026-08-10T11:46:00Z"),
      );
    });

    // The instant, the way the container list's Last seen sorts the same
    // field -- not the raw ISO string, which orders by an offset nothing
    // guarantees. Null goes to the unknown group, which Table always sorts
    // last: a host nobody has ever heard from is not the longest-silent host
    // on the page.
    it("sorts on the instant, and puts a never-seen host in the unknown group", () => {
      const col = hostColumns("1h").find((c) => c.header === "Last seen")!;
      expect(
        col.sortValue!(makeRow({ last_seen: "2026-08-10T11:46:00Z" })),
      ).toBe(Date.parse("2026-08-10T11:46:00Z"));
      // Same instant, written at a different offset: it must sort to the same
      // place, which sorting the string could not promise.
      expect(
        col.sortValue!(makeRow({ last_seen: "2026-08-10T13:46:00+02:00" })),
      ).toBe(col.sortValue!(makeRow({ last_seen: "2026-08-10T11:46:00Z" })));
      expect(col.sortValue!(makeRow({ last_seen: null }))).toBeNull();
      expect(col.sortValue!(makeRow({ last_seen: "not a date" }))).toBeNull();
    });
  });
  // The cell refuses to print a figure for a host that stopped reporting, so
  // the column must refuse to order on one: a machine that died at 90% sorted
  // above every live host in the fleet while showing nothing at all.
  describe("sorting a stale host", () => {
    it("orders CPU and Memory as unknown once a host stops reporting", () => {
      const cols = hostColumns("1h");
      const cpu = cols.find((c) => c.header === "CPU")!;
      const memory = cols.find((c) => c.header === "Memory")!;
      const stale = makeRow({ last_seen: "2020-01-01T00:00:00Z" });
      const live = makeRow({ last_seen: new Date().toISOString() });

      expect(cpu.sortValue!(stale)).toBeNull();
      expect(cpu.sortValue!(live)).not.toBeNull();
      expect(memory.sortValue!(stale)).toBeNull();
      expect(memory.sortValue!(live)).not.toBeNull();
    });
  });
});
