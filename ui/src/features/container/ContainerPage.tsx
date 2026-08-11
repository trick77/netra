// Container detail (spec 5.3). Four small multiples from the same ChartPanel
// the host Graphs tab uses -- a container is another entity with a time
// series and earns no bespoke chart -- then Identity and Not collected.
//
// The page takes its data and its range as props rather than fetching:
// Wave 5 owns the router and the polling loop, and a page that fetches for
// itself cannot be driven from a URL.
import { Badge, type Severity } from "../../ui/Badge";
import { Card } from "../../ui/Card";
import { Meter } from "../../ui/Meter";
import { Segmented } from "../../ui/Segmented";
import { ChartPanel, type Band } from "../../ui/charts/ChartPanel";
import type { Container, MetricsResponse } from "../../lib/api";
import {
  hasGaps,
  seriesTimestamps,
  griddedValues,
  windowNotice,
} from "../../lib/metrics";
import { ABSENT, bytes, percent, relativeMs } from "../../lib/format";
import type { Range } from "../../lib/range";

// The windows this page OFFERS. The type is lib/range's, so a range chosen
// anywhere else -- Settings' stored default, a link from the host page --
// can still be handed here; a container's series are the same metrics
// families the host Graphs tab draws, so the same three windows fit.
const RANGES: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
];

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
  silentAfterS = SILENT_AFTER_S,
}: DerivedStateInput): DerivedState {
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
    timestamps: seriesTimestamps(res, i),
  };
}

/** Bytes per second, not bits: net_rx/net_tx and io_read/io_write are byte
 * counters divided by the scrape interval (agent/collector/containers.go),
 * and handing them to format.ts's bitrate() would be the 8x-wrong number
 * that still looks plausible. */
function perSecond(n: number | null): string {
  return n === null ? ABSENT : `${bytes(n)}/s`;
}

function last(values: readonly (number | null)[]): number | null {
  return values.filter((v): v is number => v !== null).at(-1) ?? null;
}

export interface ContainerPageProps {
  container: Container;
  host: { id: number; hostname: string };
  /** A family=container response. The series for this container is picked
   * out by its key, so a whole-host response is as acceptable as a narrowed
   * one. */
  metrics: MetricsResponse;
  range: Range;
  onRangeChange: (range: Range) => void;
  /** Injectable so "last sample" is deterministic in tests. */
  now?: Date;
}

export function ContainerPage({
  container,
  host,
  metrics,
  range,
  onRangeChange,
  now = new Date(),
}: ContainerPageProps) {
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
  });

  // Overlay (below ChartPanel) legends any panel with two or more series
  // itself, so rx/tx and read/write are named without this page drawing a
  // legend of its own -- colour alone never carries series identity.
  const band = (
    name: string,
    color: string,
    values: (number | null)[],
  ): Band => ({ name, color, values });
  const empty: (number | null)[] = [];

  return (
    <>
      <div className="hosthead">
        <h1>{displayTitle(container)}</h1>
        <div className="meta">
          <a href={`/hosts/${host.id}`}>{host.hostname}</a>
          {" · "}
          {container.image ?? ABSENT}
        </div>
        <Badge severity={state.severity}>{state.label}</Badge>
        {/* The badge reports a measurement; this word says the state behind
            it was inferred from samples rather than read from Docker. */}
        <span className="meta" title={state.why}>
          derived from samples
        </span>
        <Segmented options={RANGES} value={range} onChange={onRangeChange} />
      </div>

      {/* .sm is index.css's small-multiples grid; .smp is what ChartPanel
          renders into it. */}
      <div className="sm">
        <ChartPanel
          title="CPU"
          fmt={(n) => percent(n)}
          notice={notice}
          window={metrics.window}
          range={range}
          onRangeChange={onRangeChange}
          series={[band("cpu", "var(--s1)", sampled?.cpu ?? empty)]}
        />
        <ChartPanel
          title="Memory"
          fmt={bytes}
          notice={notice}
          window={metrics.window}
          range={range}
          onRangeChange={onRangeChange}
          max={memLimit ?? undefined}
          series={[band("used", "var(--s2)", sampled?.memUsed ?? empty)]}
        />
        <ChartPanel
          title="Network"
          fmt={perSecond}
          notice={notice}
          window={metrics.window}
          range={range}
          onRangeChange={onRangeChange}
          series={[
            band("rx", "var(--s1)", sampled?.netRx ?? empty),
            band("tx", "var(--s3)", sampled?.netTx ?? empty),
          ]}
        />
        <ChartPanel
          title="Disk I/O"
          fmt={perSecond}
          notice={notice}
          window={metrics.window}
          range={range}
          onRangeChange={onRangeChange}
          series={[
            band("read", "var(--s2)", sampled?.ioRead ?? empty),
            band("write", "var(--s4)", sampled?.ioWrite ?? empty),
          ]}
        />
      </div>

      {/* Meter renders its own .mrow; the bar (or the words "no limit")
          belongs directly under the memory chart it qualifies. */}
      <div>
        <Meter
          value={memUsed}
          max={memLimit}
          // "no limit" is a fact about a container that reported mem_limit
          // as null; a container that reported nothing at all has not said
          // it is unlimited, and gets the absent marker instead.
          noLimit={sampled !== null && memLimit === null}
          label="Memory against mem_limit"
          formatValue={(value, max, pct) =>
            `${bytes(value)} of ${bytes(max)} (${percent(pct)})`
          }
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
              <a href={`/hosts/${host.id}`}>{host.hostname}</a>
            </dd>
            <dt>is_agent</dt>
            <dd>{container.is_agent ? "yes" : "no"}</dd>
            <dt>Last sample</dt>
            <dd>{relativeMs(lastSampleMs, now)}</dd>
          </dl>
        </Card>

        {/* Spec 11: the agent reads health and labels from the Docker
            socket, but ContainerSample carries only container_key, name,
            image, is_agent and the six metrics, and the containers table
            stores the first four. Naming them is the point -- a field that
            is simply absent from a UI reads as a field that is fine. */}
        <Card title="Not collected">
          <dl className="kv">
            <dt>Health</dt>
            <dd>
              read from the Docker socket by the agent, never sent to the hub
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
