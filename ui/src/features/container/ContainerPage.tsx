// Container detail (spec 5.3). Four small multiples from the same ChartPanel
// the host Graphs tab uses -- a container is another entity with a time
// series and earns no bespoke chart -- then Identity and Not collected.
//
// The page takes its data and its range as props rather than fetching:
// Wave 5 owns the router and the polling loop, and a page that fetches for
// itself cannot be driven from a URL.
import { Fragment, useState } from "react";
import { Badge, type Severity } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { containerIsGone, containerSamplesBlocked } from "./columns";
import { purgeContainer } from "../../lib/api";
import { Card } from "../../ui/Card";
import { Meter } from "../../ui/Meter";
import { Segmented } from "../../ui/Segmented";
import { ChartPanel, type Band } from "../../ui/charts/ChartPanel";
import type { Container, MetricsResponse } from "../../lib/api";
import { hostStatus } from "../../lib/host";
import { deriveState, DOCKER_STATED_KINDS } from "./state";
import {
  hasInteriorGaps,
  hasReading,
  latestValue,
  seriesTimestamps,
  griddedValues,
  carriesColumn,
  windowNotice,
} from "../../lib/metrics";
import {
  ABSENT,
  byterate,
  bytes,
  bytesPair,
  percent,
  relativeMs,
} from "../../lib/format";
import { RAIL_RANGES, type Range } from "../../lib/range";
import { Box } from "lucide-react";

// The windows this page OFFERS. The type is lib/range's, so a range chosen
// anywhere else -- Settings' stored default, a link from the host page --
// can still be handed here; a container's series are the same metrics
// families the host Graphs tab draws, so the same three windows fit.
export const CONTAINER_RANGES: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
];

/** The same set as the bare values clampRange takes. */
export const CONTAINER_RANGE_VALUES: readonly Range[] = CONTAINER_RANGES.map(
  (o) => o.value,
);

// A Docker id, which containerKey() falls back to only after compose labels
// AND the container name are both missing (agent/collector/containers.go).
// A 64-hex string is not a name and must never be a Display heading.
const DOCKER_ID = /^[0-9a-f]{12,64}$/;

/**
 * The heading. `container_key` is ALREADY compose project + service --
 * ingest.proto's ContainerSample says so and containerKey() builds it that
 * way -- so this is a fallback chain, not a parse. The id is the only thing
 * excluded: it changes on every recreate and identifies nothing a reader
 * recognises.
 */
export function displayTitle(container: Container): string {
  if (DOCKER_ID.test(container.container_key)) {
    return container.name ?? ABSENT;
  }
  return container.container_key;
}

/**
 * What a field reads as when Docker was never asked.
 *
 * Deliberately not ABSENT's bare dash. A dash beside "Health" is read as "no
 * health problem", which is the exact misreading the Not collected card was
 * written to prevent; this says which of the two it is. It appears whenever
 * the agent has no Docker socket, is older than the release that sends these,
 * or -- for Restarts alone -- has a socket the daemon will not let it inspect.
 */
const notReported = "not reported";

/** A label the daemon reports with an empty value. Printing nothing would make
 * the row look like a rendering bug rather than the label it is. */
const EMPTY_LABEL = "(empty)";

/**
 * The only range on this page that carries a restart series, named the way the
 * range picker names it so the card and the buttons above it agree.
 *
 * It is the RAW tier, and which tier answers is decided by the requested STEP
 * rather than by how far back the range reaches: lib/range.ts asks for 60s at
 * 1h and 5m at both 6h and 24h, and selectTier takes the coarsest tier at or
 * below the step. So 6h already resolves to container_samples_5m, where
 * restart_count does not exist (migration 0012). Naming 6h here would promise
 * a series on a range that has none.
 */
const RESTART_SERIES_RANGE: Range = "1h";

/**
 * Docker's health, in words rather than in Docker's vocabulary.
 *
 * Only "none" is translated. It is the commonest value on a real host -- most
 * images define no HEALTHCHECK -- and printed raw it reads as a verdict
 * ("health: none") rather than as the absence of a test. The other three are
 * already English and are Docker's own terms, which is what an operator will
 * grep their `docker ps` output for.
 *
 * null passes through as null, for the caller to render as notReported: that
 * is the different fact that nobody could look.
 */
export function healthText(health: string | null): string | null {
  if (health === null) return null;
  return health === "none" ? "no healthcheck" : health;
}

type Sampled = {
  cpu: (number | null)[];
  memUsed: (number | null)[];
  memLimit: (number | null)[];
  netRx: (number | null)[];
  netTx: (number | null)[];
  ioRead: (number | null)[];
  ioWrite: (number | null)[];
  cpuUser: (number | null)[];
  cpuSystem: (number | null)[];
  memAnon: (number | null)[];
  memFile: (number | null)[];
  memShmem: (number | null)[];
  memKernel: (number | null)[];
  /** Docker's restart counter, or null throughout when this tier does not
   * carry it -- it lives in the raw table only, deliberately (migration 0012).
   * A rolled-up range therefore knows nothing about restarts, which is a
   * different fact from a container that did not restart, and the two must
   * not be collapsed. */
  restartCount: (number | null)[] | null;
  timestamps: number[];
};

// The family's key is named "container" and holds the container_key
// (internal/hub/read/family.go's keySpec{name: "container", expr:
// "d.container_key"} -- the name, not the SQL expression, is what reaches
// Series.Key). Selecting series[0] instead would chart a neighbouring
// container's CPU under this container's heading whenever the caller hands
// over an unnarrowed host response: silently wrong, with nothing to see.
const KEY = "container";

// A container the hub has never sampled -- or one absent from this response
// -- has no series here, and seriesValues() would throw SeriesIndexError on
// it. That is not a programmer error, it is a real and expected state, so it
// is checked rather than caught.
function read(res: MetricsResponse, containerKey: string): Sampled | null {
  const i = res.series.findIndex((s) => s.key[KEY] === containerKey);
  if (i === -1) return null;
  // griddedValues, not seriesValues: the response carries only the buckets
  // that exist, so a container that stopped reporting arrives as a shorter
  // series and the geometry, which breaks a line only on a null, drew one
  // straight across the hole. "The container was gone" must not render as
  // "its memory was flat".
  return {
    cpu: griddedValues(res, i, "cpu_pct"),
    memUsed: griddedValues(res, i, "mem_used"),
    memLimit: griddedValues(res, i, "mem_limit"),
    netRx: griddedValues(res, i, "net_rx"),
    netTx: griddedValues(res, i, "net_tx"),
    ioRead: griddedValues(res, i, "io_read"),
    ioWrite: griddedValues(res, i, "io_write"),
    cpuUser: griddedValues(res, i, "cpu_user"),
    cpuSystem: griddedValues(res, i, "cpu_system"),
    memAnon: griddedValues(res, i, "mem_anon"),
    memFile: griddedValues(res, i, "mem_file"),
    memShmem: griddedValues(res, i, "mem_shmem"),
    memKernel: griddedValues(res, i, "mem_kernel"),
    // Guarded, unlike every column above it. Those exist at every tier, so
    // asking for one that is missing is a programmer error and
    // UnknownColumnError is the right answer. restart_count is missing at 5m,
    // 1h and 1d BY DESIGN, so asking blind would throw on every range but the
    // 1h one -- a blank page for a working feature. See
    // RESTART_SERIES_RANGE for why 6h is already a rollup.
    restartCount: carriesColumn(res, "restart_count")
      ? griddedValues(res, i, "restart_count")
      : null,
    timestamps: seriesTimestamps(res, i),
  };
}

/**
 * How far Docker's restart counter moved across the window on screen.
 *
 * The DIFFERENCE, not the total, because the only question it answers here is
 * whether a hole in the series is a restart. A container's lifetime total says
 * nothing about the window being looked at.
 *
 * A decrease returns 0 rather than a negative: Docker resets RestartCount when
 * a container is recreated, and container_key is the compose service, so a
 * redeploy walks the counter backwards. "Restarted -6 times" is not a
 * sentence, and a redeploy is not the restart this is trying to explain.
 *
 * Null when the tier carries no counter at all, which the caller renders as
 * "not available for this range" rather than as "did not restart".
 */
export function restartsInWindow(sampled: Sampled | null): number | null {
  if (!sampled?.restartCount) return null;
  const seen = sampled.restartCount.filter((v): v is number => v !== null);
  if (seen.length < 2) return null;
  return Math.max(0, seen[seen.length - 1] - seen[0]);
}

export interface ContainerBands {
  cpuBands: Band[];
  memBands: Band[];
  netBands: Band[];
  ioBands: Band[];
}

/**
 * Every band this page draws, from one container's samples.
 *
 * Hoisted out of the component so an enlarged chart fetching its own wider
 * window rebuilds its bands exactly as the small panel built them -- the
 * split-versus-total fallbacks below are a real decision about what the data
 * says, and a dialog that skipped them would draw a different chart from the
 * one it was opened on.
 *
 * Overlay (below ChartPanel) legends any panel with two or more series
 * itself, so rx/tx and read/write are named without this page drawing a
 * legend of its own -- colour alone never carries series identity.
 */
function bandsFor(sampled: Sampled | null): ContainerBands {
  const band = (
    name: string,
    color: string,
    values: (number | null)[],
  ): Band => ({ name, color, values });
  const empty: (number | null)[] = [];

  // A band whose column the answering tier does not carry comes back empty,
  // and one the container never reported comes back all null. Neither is a
  // band: in a STACK the second is worse than useless, because stackBands
  // breaks every band at any index where any series is null. hasReading() is
  // that test, shared with lib/bands.ts so the host's memory stack and this
  // one drop a band on the same rule.
  const cpuSplit = [
    band("user", "var(--s1)", sampled?.cpuUser ?? empty),
    // --s2, not --s7: orange is attention's hue, and a container burning CPU
    // in system time is not a severity netra has decided. Blue over green is
    // also the pair the host's own CPU stack opens with, so the two charts
    // agree about what user and system look like.
    band("system", "var(--s2)", sampled?.cpuSystem ?? empty),
  ].filter((b) => hasReading(b.values));
  const cpuBands =
    cpuSplit.length > 1
      ? cpuSplit
      : [band("cpu", "var(--s1)", sampled?.cpu ?? empty)];

  const memSplit = [
    band("anon", "var(--s1)", sampled?.memAnon ?? empty),
    band("file", "var(--s2)", sampled?.memFile ?? empty),
    band("shmem", "var(--s4)", sampled?.memShmem ?? empty),
    band("kernel", "var(--s8)", sampled?.memKernel ?? empty),
  ].filter((b) => hasReading(b.values));
  // The unsplit fallback is mem_used, which is exactly what this container's
  // sparkline cell and the host's Docker Memory panel draw -- so it wears
  // their colour. The split above still names its four kinds of byte from the
  // series ramp; giving THAT its own memory family is a separate decision,
  // and it is the host stack's --mem-* question, not this one.
  const memBands =
    memSplit.length > 1
      ? memSplit
      : [band("used", "var(--cmem-1)", sampled?.memUsed ?? empty)];

  return {
    cpuBands,
    memBands,
    // Mirrored about a midline, ingress above and egress below, like every
    // other traffic chart in the app. Green over purple for the same reason
    // the fleet row uses them: against green, blue separates by CVD dE 9 and
    // the two halves read as one mass.
    netBands: [
      band("in", "var(--s2)", sampled?.netRx ?? empty),
      band("out", "var(--s5)", sampled?.netTx ?? empty),
    ],
    ioBands: [
      band("read", "var(--s2)", sampled?.ioRead ?? empty),
      band("write", "var(--s4)", sampled?.ioWrite ?? empty),
    ],
  };
}

function last(values: readonly (number | null)[]): number | null {
  return values.filter((v): v is number => v !== null).at(-1) ?? null;
}

/**
 * What each `container_network` capability value means, in the reader's
 * terms rather than the kernel's. Both are failures, and both name a remedy,
 * because a sentence an operator cannot act on is the bug these replaced:
 *
 *   namespaced      the container was started without `pid: host`. The setup
 *                   script now renders it unconditionally, so re-running it
 *                   is the fix -- named here for the same reason
 *                   CGROUP_REMEDY names it in lib/containers.ts.
 *   no-host-netns   the namespace IS present and the link still would not
 *                   read, which in practice is the kernel's ptrace access
 *                   check: it needs CAP_SYS_PTRACE for a non-dumpable target
 *                   even when the uids match, and `no-new-privileges` makes
 *                   every target non-dumpable. Re-running the script changes
 *                   nothing, so it points at the Docker socket instead --
 *                   which answers host-vs-bridged outright and is the only
 *                   path to this state, since a host WITH the socket never
 *                   reaches the namespace comparison at all.
 *
 * Values mirror capNetNamespaced and capNetNoHostNS in
 * internal/agent/collector/containers.go, which picks between them from
 * AGENT_PID_HOST rather than from an errno -- the two failures are
 * indistinguishable at the syscall.
 */
const NETWORK_UNAVAILABLE: Record<string, string> = {
  "no-host-netns":
    "The agent could not read this host's network namespaces, so it cannot tell a host-networked container from a bridged one and measured no container traffic. Mounting the Docker socket answers that question without the kernel access this needs.",
  namespaced:
    "The agent is running without the host's PID namespace, so it cannot resolve the processes that own each container's interfaces and measured no container traffic. Re-run setup-agent.sh on this host.",
};

export interface ContainerPageProps {
  container: Container;
  host: {
    id: number;
    hostname: string;
    last_seen: string | null;
    capabilities?: Record<string, string>;
  };
  /** A family=container response. The series for this container is picked
   * out by its key, so a whole-host response is as acceptable as a narrowed
   * one. */
  metrics: MetricsResponse;
  range: Range;
  onRangeChange: (range: Range) => void;
  /** Loads a family=container response at another range, for an enlarged
   * chart alone. Without it the dialogs carry no picker -- a page that
   * cannot refetch has nothing to offer one. */
  fetchMetrics?: (range: Range) => Promise<MetricsResponse>;
  /**
   * The host's `container_network` capability, when it reported one.
   *
   * The key is only ever present to say that per-container networking
   * produced NOTHING -- containers.go's setCapability is documented as
   * recording "why per-container networking produced nothing", and Collect
   * clears it on every scrape that works. So any value here means net_rx
   * and net_tx are absent for every container on this host, a fact about
   * the agent's access rather than about the container's traffic. Without
   * it the Network panel is an empty chart, which claims the container sent
   * nothing.
   *
   * Both values are failures, and "namespaced" is the easier one to
   * misread as healthy: it means cgroup.procs names host PIDs that the
   * agent, running without the host PID namespace, resolves in its own and
   * finds nothing.
   */
  containerNetwork?: string;
  /** Injectable so "last sample" is deterministic in tests. */
  now?: Date;
  /**
   * Called after this container has been purged, so the caller can leave a
   * page whose subject no longer exists.
   *
   * Absent, no purge control is offered at all -- which is what a caller
   * with nowhere to go afterwards should do.
   */
  onPurged?: () => void;
}

export function ContainerPage({
  container,
  host,
  metrics,
  range,
  onRangeChange,
  fetchMetrics,
  containerNetwork,
  now = new Date(),
  onPurged,
}: ContainerPageProps) {
  // Two-step, like the host admin table's Delete / Confirm delete: this app
  // has no modal, and one click that deletes a container's history would be
  // the only such click in it.
  const [confirming, setConfirming] = useState(false);
  const [purging, setPurging] = useState(false);
  const [purgeError, setPurgeError] = useState<string | null>(null);
  const sampled = read(metrics, container.container_key);
  const notice = windowNotice(metrics);

  // mem_limit is a nullable column, present on every point: a container with
  // no limit reports null forever rather than dropping the column. So "no
  // limit" is an all-null series, and a MISSING column stays what metrics.ts
  // calls it -- a programmer error, not a product state.
  const memLimit = sampled ? last(sampled.memLimit) : null;
  const memUsed = sampled ? last(sampled.memUsed) : null;
  // containers.last_seen, NOT the tail of the fetched series.
  //
  // They are two different clocks and they disagree. last_seen is the hub's
  // record of when a sample for this container last landed; the series is one
  // read of one window through whatever tier that window resolves to, and it
  // can trail the listing by hours. Driven by the series, this page called a
  // container Silent while the fleet list -- which reads last_seen -- showed
  // it reporting, one click apart. The lists cannot use the series (a fan-out
  // has no window per container), so the page uses what both surfaces have.
  //
  // The series still decides the two states that are ABOUT a series: a hole
  // in it, and memory against the limit.
  // A window with no series at all still reads "No samples": that is a fact
  // about the range on screen, and "Reporting" over four empty charts would
  // be the page contradicting itself. The lists have no window and so never
  // reach that state, which is honest -- they are not showing one.
  //
  // Unless the container is gone, which deriveState tests above "No samples".
  // Empty charts at the 1h range on a container that stopped two hours ago
  // are empty BECAUSE it is gone, and "No samples" above a button offering to
  // purge it says less than the button does.
  //
  // And nothing at all on a host whose agent cannot see containers: it keeps
  // posting host samples while no container sample can land, so last_seen
  // ages forever. containerIsGone returns false on exactly that host and this
  // page must not call the container Silent instead.
  const lastSeenMs = Date.parse(container.last_seen);
  const lastSampleMs =
    sampled &&
    !Number.isNaN(lastSeenMs) &&
    !containerSamplesBlocked(host.capabilities?.containers)
      ? lastSeenMs
      : null;

  // Gone, by the same rule the lists use: this container stopped being
  // reported while its host kept reporting. Measured against the host, never
  // against the clock, so an offline host offers no purge for anything on it.
  //
  // Computed before the badge because the badge defers to it: the purge
  // button below is offered on this exact condition, and a badge that
  // disagreed with the button beside it is the whole bug.
  const gone = containerIsGone({
    ...container,
    host_id: host.id,
    hostname: host.hostname,
    host_last_seen: host.last_seen,
    host_containers_capability: host.capabilities?.containers,
  });

  const state = deriveState({
    lastSampleMs,
    memUsed,
    memLimit,
    gap: sampled ? hasInteriorGaps(sampled.cpu) : false,
    now,
    hostState: hostStatus(host, now),
    gone,
    dockerState: container.docker_state,
    health: container.health,
    restartsInWindow: restartsInWindow(sampled),
  });

  const { cpuBands, memBands, netBands, ioBands } = bandsFor(sampled);

  async function onPurge() {
    setPurgeError(null);
    if (!confirming) {
      setConfirming(true);
      return;
    }
    setConfirming(false);
    setPurging(true);
    try {
      await purgeContainer(host.id, container.id);
      onPurged?.();
    } catch (err) {
      setPurgeError(err instanceof Error ? err.message : String(err));
    } finally {
      setPurging(false);
    }
  }

  // One family, one other range, for an enlarged chart alone -- the page
  // keeps showing what its own picker asked for. Each panel narrows the
  // response to THIS container and rebuilds its bands through the same
  // bandsFor the page uses, so a widened dialog draws what the small panel
  // drew, split bands and fallbacks included.
  const detail = fetchMetrics
    ? (pick: (b: ContainerBands) => Band[]) => async (next: Range) => {
        const answered = await fetchMetrics(next);
        return {
          series: pick(bandsFor(read(answered, container.container_key))),
          window: answered.window ?? null,
        };
      }
    : () => undefined;

  return (
    <>
      <div className="hosthead">
        {/* Box, not the rail's LayoutGrid: the four tiles say "the set of
            containers", and this page is one of them. */}
        <span className="pageicon">
          <Box aria-hidden="true" />
        </span>
        <h1>{displayTitle(container)}</h1>
        <div className="meta">
          <a href={`/hosts/${host.id}/overview`}>{host.hostname}</a>
          {" · "}
          {container.image ?? ABSENT}
        </div>
        <Badge severity={state.severity}>{state.label}</Badge>
        {/* Which of the two the badge is. It used to always say "derived from
            samples", which was true of every state there was; three of them
            are now Docker's own word, and a badge quoting the daemon must not
            claim to have inferred it -- the caption exists to tell a reader how
            much to trust the badge, so a wrong one is worse than none. */}
        <span className="meta" title={state.why}>
          {DOCKER_STATED_KINDS.has(state.kind)
            ? "read from Docker"
            : "derived from samples"}
        </span>
        <Segmented
          options={CONTAINER_RANGES}
          value={range}
          onChange={onRangeChange}
        />
        {/* Only once the container is gone, and only when the caller can
            navigate away afterwards. A running container has nothing to
            purge: the next scrape recreates the row, minus its history. */}
        {gone && onPurged !== undefined ? (
          <Button
            small
            variant={confirming ? "danger" : undefined}
            busy={purging}
            onClick={() => void onPurge()}
            title={
              confirming
                ? "Deletes this container's row and its stored CPU and memory history"
                : undefined
            }
          >
            {confirming ? "Confirm purge" : "Purge container"}
          </Button>
        ) : null}
      </div>
      {purgeError === null ? null : (
        <p className="note">Purge failed: {purgeError}</p>
      )}

      {/* .sm is index.css's small-multiples grid; .smp is what ChartPanel
          renders into it. */}
      <div className="sm">
        {/* The split when the kernel reported it, the total when it did not.
            user and system sum to cpu_pct, so stacking them says the same
            thing the one line said plus where the time went -- a service
            pinned in system time is contending on the kernel, which is a
            different problem from one pinned in user time. */}
        <ChartPanel
          title="CPU"
          fmt={(n) => percent(n)}
          notice={notice}
          window={metrics.window}
          range={range}
          ranges={RAIL_RANGES}
          fetchSeries={detail((b) => b.cpuBands)}
          stacked={cpuBands.length > 1}
          series={cpuBands}
        />
        {/* memory.stat's parts rather than one number: a container holding
            2 GB of heap and one that read 2 GB of files look identical in
            mem_used, and only the first is a limit that needs raising. The
            limit is the ceiling and its own dashed rule, so "how close is
            this to being OOM-killed" is answerable from the chart. */}
        <ChartPanel
          title="Memory"
          fmt={bytes}
          notice={notice}
          window={metrics.window}
          range={range}
          ranges={RAIL_RANGES}
          fetchSeries={detail((b) => b.memBands)}
          max={memLimit === null ? undefined : memLimit * 1.08}
          reference={memLimit ?? undefined}
          // The limit names itself in the header, beside the reading it is the
          // limit for, rather than as text over the rule inside the plot.
          nowFmt={(n) => bytesPair(n, memLimit)}
          // ...and the reading it is the limit for is the container's WHOLE
          // memory, not series[0]'s. Split into anon/file/shmem/kernel,
          // series[0] is the anon band alone, so the header paired one of
          // four bands against the limit -- "0.5 · 1 GB" for a container at
          // 0.9 GB of its 1 GB, which reads as half the limit used when it is
          // about to be OOM-killed. mem_used is the number the pair claims to
          // be, and it is what the meter directly below already shows.
          nowValue={
            // memBands, not memSplit: the split and the `empty` sentinel are
            // locals of bandsFor(), and referencing them from here compiled
            // only while that code lived in this scope. The two say the same
            // thing by construction -- memBands IS memSplit when the split
            // survived, and a single "used" band when it did not.
            memBands.length > 1
              ? latestValue(sampled?.memUsed ?? [])
              : undefined
          }
          stacked={memBands.length > 1}
          series={memBands}
          // The bar belongs to THIS chart, so it is drawn inside this panel.
          // It used to sit in its own row after the small-multiples grid, and
          // a grid is not a column: `.sm` is auto-fill, so the row after it
          // follows the LAST panel, which is Disk I/O at every width the grid
          // wraps at. The comment there claimed it was "directly under the
          // memory chart it qualifies"; it never was.
          footer={
            <Meter
              value={memUsed}
              max={memLimit}
              // "no limit" is a fact about a container that reported mem_limit
              // as null; a container that reported nothing at all has not said
              // it is unlimited, and gets the absent marker instead.
              noLimit={sampled !== null && memLimit === null}
              // No label: the panel title says Memory, and the header above
              // already carries "used · limit" through nowFmt. Repeating
              // either inside the same card is noise. The percentage is what
              // the bar adds -- it is the one form of the reading neither the
              // header nor the dashed ceiling rule states.
              formatValue={(_value, _max, pct) => percent(pct)}
            />
          }
        />
        {/* Mirrored about a midline, ingress above and egress below, like
            every other traffic chart in the app -- two lines climbing one
            axis make a reader compare shapes to answer which way the traffic
            is going. Green over purple for the same reason the fleet row
            uses them: against green, blue separates by CVD dE 9 and the two
            halves read as one mass. */}
        <ChartPanel
          title="Network"
          fmt={byterate}
          notice={notice}
          window={metrics.window}
          range={range}
          ranges={RAIL_RANGES}
          fetchSeries={detail((b) => b.netBands)}
          mirrored
          // The agent's own explanation, in place of a chart that would
          // otherwise read as "this container moved no traffic".
          //
          // ANY value blanks the panel, because the key exists only to
          // report that networking produced nothing -- a working collector
          // reports no key at all. Matching one value left the other one,
          // "namespaced", drawing the empty chart this prop exists to
          // prevent. An unrecognised value still blanks it and says what
          // the agent said: a capability netra does not know the wording of
          // is still the agent reporting a failure.
          unavailable={
            containerNetwork === undefined
              ? undefined
              : (NETWORK_UNAVAILABLE[containerNetwork] ??
                `The agent reported per-container networking as "${containerNetwork}", so no container traffic was measured.`)
          }
          series={netBands}
        />
        <ChartPanel
          title="Disk I/O"
          fmt={byterate}
          notice={notice}
          window={metrics.window}
          range={range}
          ranges={RAIL_RANGES}
          fetchSeries={detail((b) => b.ioBands)}
          series={ioBands}
        />
      </div>

      <div className="grid2">
        <Card title="Identity">
          <dl className="kv">
            <dt>container_key</dt>
            <dd>{container.container_key}</dd>
            <dt>name</dt>
            <dd>{container.name ?? ABSENT}</dd>
            <dt>image</dt>
            <dd>{container.image ?? ABSENT}</dd>
            <dt>Host</dt>
            <dd>
              <a href={`/hosts/${host.id}/overview`}>{host.hostname}</a>
            </dd>
            <dt>is_agent</dt>
            <dd>{container.is_agent ? "yes" : "no"}</dd>
            <dt>State</dt>
            <dd>{container.docker_state ?? notReported}</dd>
            <dt>Health</dt>
            <dd>{healthText(container.health) ?? notReported}</dd>
            <dt>Restarts</dt>
            <dd>{container.restart_count ?? notReported}</dd>
            <dt>Last sample</dt>
            <dd>{relativeMs(lastSampleMs, now)}</dd>
          </dl>
        </Card>

        {/* Labels get a card of their own rather than three more rows above.
            A container can carry twenty of them -- compose writes two,
            Kubernetes writes annotations through, build pipelines stamp in
            image provenance -- and twenty pairs inside the Identity list would
            bury container_key and the host link under them. */}
        <Card title="Labels">
          {container.labels === null ? (
            <p className="muted">{notReported}</p>
          ) : Object.keys(container.labels).length === 0 ? (
            /* A measurement, not an absence: the agent read this container's
               labels and it has none. Rendering it the same as "not reported"
               would throw away the distinction the wire goes out of its way to
               carry (ContainerSample.labels is a wrapper message precisely so
               an empty map and a missing one differ). */
            <p className="muted">none</p>
          ) : (
            <dl className="kv">
              {Object.entries(container.labels)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([key, value]) => (
                  <Fragment key={key}>
                    <dt>{key}</dt>
                    <dd>{value === "" ? EMPTY_LABEL : value}</dd>
                  </Fragment>
                ))}
            </dl>
          )}
        </Card>

        {/* The card that named health, restarts, state and labels as absent.
            All four are collected now -- they are on the Identity card and in
            the Labels card above -- so what is left is the residue: three
            things the collection genuinely still cannot say. The card stays,
            because that was always its point: a field simply absent from a UI
            reads as a field that is fine. */}
        <Card title="Not collected">
          <dl className="kv">
            <dt>Stopped containers</dt>
            <dd>
              a container's rows come from its cgroup, and a stopped one has
              none, so it does not appear here at all -- Docker's own "exited"
              is never seen and the header badge measures its absence instead
            </dd>
            <dt>Health history</dt>
            <dd>
              only the latest health and state are kept, so "when did it go
              unhealthy" has no answer; a transition would need its own table
            </dd>
            <dt>Restarts beyond {RESTART_SERIES_RANGE}</dt>
            <dd>
              the restart counter lives in the raw samples only, and every range
              but {RESTART_SERIES_RANGE} is answered from a rollup, so a wider
              window shows the total above but no series to place it in
            </dd>
          </dl>
        </Card>
      </div>
    </>
  );
}
