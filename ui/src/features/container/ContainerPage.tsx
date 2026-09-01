// Container detail (spec 5.3). Four small multiples from the same ChartPanel
// the host Graphs tab uses -- a container is another entity with a time
// series and earns no bespoke chart -- then Identity and Not collected.
//
// The page takes its data and its range as props rather than fetching:
// Wave 5 owns the router and the polling loop, and a page that fetches for
// itself cannot be driven from a URL.
import { useState } from "react";
import { Badge, type Severity } from "../../ui/Badge";
import { Button } from "../../ui/Button";
import { containerIsGone } from "./columns";
import { purgeContainer } from "../../lib/api";
import { Card } from "../../ui/Card";
import { Meter } from "../../ui/Meter";
import { Segmented } from "../../ui/Segmented";
import { ChartPanel, type Band } from "../../ui/charts/ChartPanel";
import type { Container, MetricsResponse } from "../../lib/api";
import { hostStatus, type HostStatus } from "../../lib/host";
import {
  hasGaps,
  hasReading,
  latestValue,
  seriesTimestamps,
  griddedValues,
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

/** A container is silent once it has missed this many seconds of samples.
 * Three scrape intervals at the 60s default: one missed post is a hiccup,
 * three in a row is the container no longer being there. */
const SILENT_AFTER_S = 180;

/** Memory this close to mem_limit is the warning spec 11 asks for -- the
 * OOM killer arrives before the bar reaches the end of its track. */
const MEM_PRESSURE_PCT = 90;

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
   * quiet stops every container on it at once. The lists have always known
   * this -- containerIsGone measures against the host's last_seen, never the
   * clock, so an offline host marks nothing gone -- while this badge measured
   * against the clock and called the same container Silent. One container,
   * two surfaces, opposite answers, and the one that blamed the container was
   * the one offering to purge its history.
   *
   * Given the host's state, the badge names the host instead. Omitted, the
   * badge behaves as it did: callers without a host status are no worse off
   * than before.
   */
  hostState?: HostStatus;
  silentAfterS?: number;
}

export interface DerivedState {
  label: string;
  severity: Severity;
  /** The inference the label deliberately does not make. */
  why: string;
}

/**
 * Container state, derived from what is collected (spec 11) -- health,
 * restarts and state itself reach neither the wire nor the schema. Every
 * label here names a MEASUREMENT: samples arrived, samples stopped, memory
 * approached its limit, the series has a hole. The likely cause goes in
 * `why`, never in the badge, because "restarted" is a guess and the badge
 * is not the place to make one.
 */
export function deriveState({
  lastSampleMs,
  memUsed,
  memLimit,
  gap,
  now,
  hostState,
  silentAfterS = SILENT_AFTER_S,
}: DerivedStateInput): DerivedState {
  // First, and above even "No samples": every branch below reads the sample
  // stream, and on a host that is not reporting there is no stream to read.
  // Neutral, not serious -- the severity belongs to the host, which carries
  // it on its own page and in its fleet row, and a second critical here would
  // count one outage twice. The host's own word is reused rather than a fifth
  // synonym invented for it.
  if (hostState !== undefined && hostState.severity === "critical") {
    return {
      label: `Host ${hostState.label}`,
      severity: "neutral",
      why: "the host stopped reporting, so nothing can be said about this container until it comes back",
    };
  }

  if (lastSampleMs === null) {
    return {
      label: "No samples",
      severity: "neutral",
      why: "nothing has been reported for this container in the selected range",
    };
  }

  const ageS = (now.getTime() - lastSampleMs) / 1000;
  if (ageS > silentAfterS) {
    return {
      label: "Silent",
      severity: "serious",
      why: "the container stopped appearing in samples; it may have been stopped or removed",
    };
  }

  if (
    memUsed !== null &&
    memLimit !== null &&
    memLimit > 0 &&
    (memUsed / memLimit) * 100 >= MEM_PRESSURE_PCT
  ) {
    return {
      label: "Near mem_limit",
      severity: "warning",
      why: "memory is approaching the configured limit; the OOM killer acts before the limit is reached",
    };
  }

  if (gap) {
    return {
      label: "Series gap",
      severity: "warning",
      why: "a hole in the series usually means a restart, but restarts are not collected, so this says only that samples are missing",
    };
  }

  return {
    label: "Reporting",
    severity: "ok",
    why: "samples are arriving on schedule",
  };
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
    timestamps: seriesTimestamps(res, i),
  };
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
  const memBands =
    memSplit.length > 1
      ? memSplit
      : [band("used", "var(--s2)", sampled?.memUsed ?? empty)];

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
  const lastSampleMs = sampled ? (sampled.timestamps.at(-1) ?? null) : null;

  const state = deriveState({
    lastSampleMs,
    memUsed,
    memLimit,
    gap: sampled ? hasGaps(sampled.cpu) : false,
    now,
    hostState: hostStatus(host, now),
  });

  const { cpuBands, memBands, netBands, ioBands } = bandsFor(sampled);

  // Gone, by the same rule the lists use: this container stopped being
  // reported while its host kept reporting. Measured against the host, never
  // against the clock, so an offline host offers no purge for anything on it.
  const gone = containerIsGone({
    ...container,
    host_id: host.id,
    hostname: host.hostname,
    host_last_seen: host.last_seen,
    host_containers_capability: host.capabilities?.containers,
  });

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
        <h1>{displayTitle(container)}</h1>
        <div className="meta">
          <a href={`/hosts/${host.id}/overview`}>{host.hostname}</a>
          {" · "}
          {container.image ?? ABSENT}
        </div>
        <Badge severity={state.severity}>{state.label}</Badge>
        {/* The badge reports a measurement; this word says the state behind
            it was inferred from samples rather than read from Docker. */}
        <span className="meta" title={state.why}>
          derived from samples
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
            <dt>Last sample</dt>
            <dd>{relativeMs(lastSampleMs, now)}</dd>
          </dl>
        </Card>

        {/* Spec 11: the agent reads labels from the Docker socket, but
            ContainerSample carries only container_key, name, image, is_agent
            and the six metrics, and the containers table stores the first
            four. Naming them is the point -- a field that is simply absent
            from a UI reads as a field that is fine. */}
        <Card title="Not collected">
          <dl className="kv">
            <dt>Health</dt>
            <dd>
              never read: the agent lists containers and decodes five fields,
              and the list endpoint carries health only inside its Status
              string, which is not one of them
            </dd>
            <dt>Restarts</dt>
            <dd>no column on the wire and none in the containers table</dd>
            <dt>State</dt>
            <dd>
              also absent; the badge in the header is inferred from the sample
              series, never read from Docker
            </dd>
            <dt>Labels</dt>
            <dd>
              only compose project and service survive, folded into
              container_key
            </dd>
          </dl>
        </Card>
      </div>
    </>
  );
}
