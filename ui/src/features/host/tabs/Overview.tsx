// The Overview tab: two or three facts from each of the other tabs plus
// what needs attention. It is deliberately a summary -- every card here
// answers "is this worth opening the tab for?", and none of them
// reproduces the tab's own content.
import { Fragment, type ReactNode } from "react";
import type {
  Container,
  HostDetail,
  MetricsResponse,
  MetricsSeries,
  Unit,
} from "../../../lib/api";
import {
  carriesColumn,
  counterIncrease,
  fsName,
  griddedValues,
  hasReading,
  latestValue,
  optionalValues,
  peakBase,
  sumSeries,
} from "../../../lib/metrics";
import {
  ABSENT,
  binaryBytes,
  binaryBytesPair,
  byterate,
  bytes,
  cardinal,
  duration,
  percent,
  relative,
} from "../../../lib/format";
import {
  FLAP_THRESHOLD,
  isReporting,
  osLabel,
  STALE_THRESHOLD_MS,
} from "../../../lib/host";
import { Badge, type Severity } from "../../../ui/Badge";
import { Card } from "../../../ui/Card";
import { Meter } from "../../../ui/Meter";
import { OsIcon } from "../../../ui/OsIcon";
import { memoryBands, perCoreBands } from "../../../lib/bands";
import { ChartPanel, type Band } from "../../../ui/charts/ChartPanel";
import { Enlargeable } from "../../../ui/charts/Enlargeable";
import { RANGE_VALUES } from "../ranges";
import type { Range } from "../../../lib/range";
import { Sparkline } from "../../../ui/charts/Sparkline";
import {
  DOWN_COLOR,
  UP_COLOR,
  UpDownSparkline,
} from "../../../ui/charts/UpDownSparkline";
// The fleet band's thresholds, imported rather than written out again. The
// comment on them in fleet/conditions.ts spells out why: this page and that
// one must agree on when a filesystem is worth mentioning, or a host warns
// in one place and reads clean in the other.
import { DISK_WARN_PCT, DISK_CRIT_PCT } from "../../fleet/conditions";

/**
 * column() in lib/metrics.ts THROWS for a column the answering tier does
 * not have, and that throw happens during render -- one absent column
 * would blank the whole tab. Every lookup on this page is therefore
 * optional by construction: a column that is not there yields no values,
 * and the card renders the absent marker instead of a fabricated number.
 *
 * The same guard exists in Graphs.tsx. It is duplicated rather than
 * extracted because lib/ is owned by another task; the two copies are
 * eight lines and identical on purpose.
 */
/** The most recent non-null reading, or null when there is none. A null
 * here means "the host reported nothing", which every caller renders as a
 * gap or as a word -- never as 0. */
function latest(
  res: MetricsResponse | null,
  base: string,
  seriesIndex = 0,
): number | null {
  const vals = optionalValues(res, seriesIndex, base);
  for (let i = vals.length - 1; i >= 0; i--) {
    const v = vals[i];
    if (v !== null) return v;
  }
  return null;
}

/**
 * The reading in the LATEST bucket of the window, or null when the series
 * does not reach it.
 *
 * latest() above answers "what did this series last say", which is right for
 * a configured ceiling and wrong for a filesystem: a row whose agent stopped
 * writing it keeps handing back its final measurement, and nothing
 * downstream can tell that from a current one. That is how one disk came to
 * warn twice under two names -- /netra/fs/ark frozen at the moment its agent
 * was upgraded, /mnt/ark live, both at 94 %, both stated as facts about now.
 *
 * griddedValues, not optionalValues, and that is the whole mechanism:
 * internal/hub/read/metrics.go emits only the rows that EXIST, so a series
 * that stopped simply ends early and its last element is its last reading,
 * indistinguishable from a current one. Placing it on the window's grid
 * turns the buckets it never reached into the nulls that say so.
 *
 * lib/metrics.ts:latestValue is the one spelling of the rule itself; see its
 * docstring on why the two questions must never share a name.
 */
function current(
  res: MetricsResponse | null,
  base: string,
  seriesIndex = 0,
): number | null {
  return latestValue(griddedValues(res, seriesIndex, base));
}

export interface FilesystemRow {
  label: string;
  total: number | null;
  used: number | null;
  free: number | null;
}

/**
 * One row per filesystem, in bytes as measured.
 *
 * No percentage is computed here, and that is a schema fact rather than a
 * layout preference: internal/hub/read/family.go records that `used` and
 * `free` do not sum to `total` (the gap is the root reserve, which is
 * neither in use nor allocatable), and that at the 5m/1h tiers a ratio
 * built from used_max and free_min composes two different instants and is
 * not the maximum of the true ratio. Absolute bytes are the only figure
 * that stays true at every tier.
 */
export function filesystemRows(res: MetricsResponse | null): FilesystemRow[] {
  // `== null`, not `=== null`: these props are optional, so a caller that
  // simply does not have this family yet passes undefined -- and reading
  // .series off it threw during render, with no error boundary under it.
  if (res == null) return [];
  return res.series.map((series, index) => ({
    // The mount point, same as the fleet row: one disk must not be called
    // /mnt/ark on one page and ark on the other.
    label: fsName(series.key, ABSENT),
    // current(), not latest(): a filesystem that has stopped reporting has no
    // fullness right now, and saying otherwise is what kept a retired row on
    // the page beside the one that replaced it. The card renders the absent
    // marker for the nulls and diskWarnings already skips them, so the disk
    // stays listed -- it is only its numbers that stop claiming to be current.
    total: current(res, "total", index),
    used: current(res, "used", index),
    free: current(res, "free", index),
  }));
}

// The latest bucket's value, trailing null included, is lib/metrics.ts's
// latestValue(). This page used to scan backwards for the last non-null,
// which reported a host that stopped an hour ago at the final rate it ever
// sent: "the agent is down" rendered as "traffic is steady at 2 MB/s". The
// fleet's traffic cell has always read the latest bucket, so the same dead
// host read as absent there and as busy here -- which is why the rule now has
// exactly one spelling instead of a copy per page.

export interface Attention {
  severity: Severity;
  what: ReactNode;
}

// Series colours are token references, never literals -- the palette lives
// in index.css and a hue chosen in a .tsx file cannot follow the theme.
const CPU_BANDS: { base: string; name: string; color: string }[] = [
  { base: "cpu_user", name: "user", color: "var(--s1)" },
  { base: "cpu_system", name: "system", color: "var(--s2)" },
  { base: "cpu_iowait", name: "iowait", color: "var(--s3)" },
  { base: "cpu_steal", name: "steal", color: "var(--s4)" },
];

/**
 * user/system/iowait/steal as bands, on the window grid.
 *
 * An all-null band is not a band, and in a STACK it is actively destructive:
 * stackBands() breaks every band at any index where any series is null,
 * because a running total is undefined there. A bare metal host reports
 * cpu_steal as NULL in every bucket -- correctly, it has no hypervisor to
 * steal from -- and that one empty series erased the whole chart, legend
 * still listing four states above a blank box.
 *
 * Shared with the enlarged view's own fetch, so widening the panel cannot
 * draw a different set of bands from the one that was clicked.
 */
function cpuStateBands(res: MetricsResponse | null): Band[] {
  return CPU_BANDS.map((band) => ({
    name: band.name,
    color: band.color,
    // On the window grid, so an outage is a hole in the silhouette rather
    // than a line drawn straight across it.
    values: griddedValues(res, 0, band.base),
  })).filter((band) => band.values.some((v) => v !== null));
}

/** The one-band silhouette: cpu_total, for a host with no per-core series.
 * One true band beats a not-collected panel where a silhouette is available. */
function totalCpuBand(values: (number | null)[]): Band[] {
  return values.length > 0
    ? [{ name: "busy", color: "var(--s1)", values }]
    : [];
}

/**
 * The sensor family's series, narrowed to the kinds one card is about.
 *
 * The original index is carried through because latest(), griddedValues()
 * and seriesTimestamps() all read by POSITION in the unfiltered series
 * list -- filtering the array without keeping the index reads another
 * sensor's readings under this sensor's name.
 *
 * kind defaults to temperature: an agent predating the field sends none,
 * and that is the only kind it could have meant.
 */
function sensorsOfKind(
  res: MetricsResponse | null,
  kinds: readonly string[],
): { series: MetricsSeries; index: number }[] {
  return (res?.series ?? [])
    .map((series, index) => ({ series, index }))
    .filter(({ series }) => kinds.includes(series.key.kind ?? "temperature"));
}

/**
 * The history one sensor row draws.
 *
 * For a FAN this is value_min and nothing else. A fan's failure is its
 * minimum: a fan that stalled for two minutes inside a five-minute bucket
 * is invisible in both the average and the maximum, and the average of a
 * stall and a spin-up is a perfectly healthy-looking number. 0001_init.sql
 * rolls value up as min as well as avg/max specifically so this is
 * answerable, and the column has to be named explicitly -- candidates() in
 * lib/metrics.ts prefers _avg, so asking for the bare name here would
 * silently hand back the one aggregate that hides the failure.
 *
 * value_min exists only at the 5m and 1h tiers; the raw table has a single
 * `value` per sample, where the reading IS its own minimum. So the fallback
 * is not a compromise -- at raw resolution there is nothing finer to ask
 * for.
 *
 * Voltages, currents and power use `value` (resolving to value_avg at the
 * rolled tiers), which is right for them: a rail sags and recovers, and the
 * bucket's mean is the honest summary of where it sat.
 */
function sensorHistory(
  res: MetricsResponse | null,
  index: number,
  kind: string,
): (number | null)[] {
  if (kind === "fan" && carriesColumn(res, "value_min")) {
    return griddedValues(res, index, "value_min");
  }
  return griddedValues(res, index, "value");
}

/** What a reader calls this sensor: chip and label, in that order.
 *
 * Also its identity across two responses -- the enlarged view re-finds its
 * own series by this name after a range change rather than by the index it
 * had, because a sensor that stopped reporting shifts every series after it
 * and the chart would silently become another sensor's. */
function sensorName(series: MetricsSeries): string {
  return (
    [series.key.chip, series.key.label].filter(Boolean).join(" ") || ABSENT
  );
}

/** The most recent non-null entry of an already-built series. The sensor
 * rows read their number off the SAME array the sparkline draws, so the
 * digits and the line can never disagree -- a fan reading 1180 RPM beside a
 * line touching zero is the exact confusion these cards exist to remove. */
function lastReading(values: readonly (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}

/** Each kind in its own unit, because the reading is meaningless without
 * it and because these share a card. Precision differs by kind: a rail at
 * 11.9 V and one at 12.0 V are different facts, while a fan at 1183 and one
 * at 1180 RPM are the same fact. */
function formatSensor(kind: string, value: number | null): string {
  if (value === null) return ABSENT;
  switch (kind) {
    case "fan":
      return `${Math.round(value)} RPM`;
    case "voltage":
      return `${value.toFixed(2)} V`;
    case "current":
      return `${value.toFixed(2)} A`;
    case "power":
      return `${value.toFixed(1)} W`;
    default:
      return `${Math.round(value)} °C`;
  }
}

/**
 * The exhaustion gauges, each with the kernel ceiling it runs against.
 *
 * The RATIO is the whole point, which is why every row here has a limit and
 * why sockets_used and tcp_alloc are absent from the list: they have no
 * ceiling in the schema, so they cannot answer "how much headroom is left"
 * and would only be two more numbers to scroll past.
 *
 * Running out of these does not present as a resource problem. accept()
 * starts failing, conntrack silently drops new flows, and the operator sees
 * a broken network on a host whose CPU and memory panels look fine -- so
 * this is the one question netra answers nowhere else.
 *
 * `capability` names the host.capabilities key whose value explains an
 * empty row: the agent says "conntrack: unavailable" when the module is not
 * loaded, and that sentence next to the empty meter is the answer, where a
 * bare em-dash is a mystery.
 */
const LIMITS: {
  label: string;
  gauge: string;
  ceiling: string;
  capability: string;
}[] = [
  {
    label: "file descriptors",
    gauge: "fd_used",
    ceiling: "fd_limit",
    capability: "file_descriptors",
  },
  {
    label: "conntrack",
    gauge: "conntrack_count",
    ceiling: "conntrack_limit",
    capability: "conntrack",
  },
  {
    label: "TIME_WAIT sockets",
    gauge: "tcp_tw",
    ceiling: "tcp_tw_limit",
    capability: "sockets",
  },
  {
    label: "orphan sockets",
    gauge: "tcp_orphan",
    ceiling: "tcp_orphan_limit",
    capability: "sockets",
  },
];

// When a host counts as stale rather than merely late. Imported, never
// restated: this used to be its own five-minute constant while hostStatus()
// used three, so a host last seen four minutes ago had its own header call it
// offline, its traffic gauges blanked, and its fleet row marked critical --
// above a panel saying nothing needed attention. lib/host.ts anchors the
// number to the product's alerting rule; there is one definition of down.

// Worst first, and the only three needsAttention() emits: `ok` is not a
// condition and `neutral` is not a severity anything here can be at. A
// severity missing from this list would drop its rows silently, which is why
// it is written out rather than derived from the data.
const ATTENTION_SEVERITIES = ["critical", "serious", "warning"] as const;

const SEVERITY_WORD: Record<Severity, string> = {
  critical: "Critical",
  serious: "Serious",
  warning: "Warning",
  ok: "OK",
  neutral: "Unknown",
};

const SEVERITY_CLASS: Record<Severity, string> = {
  critical: "st-crit",
  serious: "st-serious",
  warning: "st-warn",
  ok: "st-ok",
  neutral: "",
};

/**
 * What is wrong right now. Current state must not sit behind a tab, so this
 * is derived here from the same responses the cards above it render -- there
 * is no second source that could disagree with them.
 *
 * The order is the order it is written in, and this function sorts nothing:
 * a reader who looks twice finds the same rows in the same places. That is
 * the fleet's rule too -- see the note on hostConditions() in
 * fleet/conditions.ts.
 *
 * What renders it DOES group by severity, with a stable partition, so the
 * written order survives inside each group. Grouping is presentation; this
 * list stays as written so the presentation can change without the data
 * moving underneath it.
 */
export function needsAttention(input: {
  host: HostDetail;
  agentMetrics: MetricsResponse | null;
  hostMetrics?: MetricsResponse | null;
  filesystems: FilesystemRow[];
  units: Unit[] | null;
  now?: Date;
}): Attention[] {
  const out: Attention[] = [];
  const now = input.now ?? new Date();

  const dropped = latest(input.agentMetrics, "buffer_dropped_total");
  if (dropped !== null && dropped > 0) {
    // The agent's ring buffer overflowed: samples that were collected were
    // never delivered. Nothing else netra reports is more important, and
    // no chart can show it, because the missing data is the evidence.
    out.push({
      severity: "critical",
      what: `${dropped} ${dropped === 1 ? "sample" : "samples"} dropped before delivery — this host's history has holes`,
    });
  }

  // The kernel killed something to stay alive. This is the one memory fact
  // no chart can carry: mem_used is back to normal by the time anyone
  // looks, precisely BECAUSE the kill happened, so a host that OOM-killed
  // its database at 04:00 shows a calm memory panel at 09:00. It belongs
  // here, as an event that occurred, and not on an axis.
  //
  // The increase across the window, never the raw total: oom_kill_total is
  // cumulative since boot, so a host that killed one process a year ago
  // would otherwise carry a permanent badge. counterIncrease returns null
  // when no usable pair exists, which is "we cannot say" and stays silent
  // -- distinct from 0, which is a host confirming nothing happened.
  const oomKills = counterIncrease(
    griddedValues(input.hostMetrics ?? null, 0, "oom_kill_total"),
  );
  if (oomKills !== null && oomKills > 0) {
    out.push({
      severity: "critical",
      what: `${oomKills} OOM ${oomKills === 1 ? "kill" : "kills"} in this window — the kernel killed processes to reclaim memory`,
    });
  }

  // The increase across the window, for the same reason as the OOM block
  // above: post_failures_total is cumulative for the whole life of the agent
  // PROCESS and is deliberately never reset by a success (see the comment on
  // postFailures in internal/agent/client/client.go), and the agent re-sends
  // it on every scrape. Read with latest() it was the permanent badge the OOM
  // comment warns about -- one hub restart pinned "1 failed deliveries" to
  // the page forever, even though the ring buffer replayed those samples the
  // moment the hub came back and nothing was actually lost.
  //
  // counterDeltas drops a negative step, so the counter going back to zero on
  // an agent restart is skipped rather than counted as a huge recovery.
  const failures = counterIncrease(
    griddedValues(input.agentMetrics, 0, "post_failures_total"),
  );
  if (failures !== null && failures > 0) {
    out.push({
      severity: "warning",
      what: `${failures} failed ${failures === 1 ? "delivery" : "deliveries"} to the hub in this window`,
    });
  }

  // `critical`, not `serious`, and that is the fleet page's word for this
  // exact fact: hostConditions() in fleet/conditions.ts has always rated a
  // host that stopped reporting `critical`. The two pages used to print
  // different severities for one condition, so the same host read "serious"
  // here and "critical" one click up -- the kind of disagreement the shared
  // disk thresholds below exist to prevent, in the one place a constant
  // could not fix it. `serious` remains a Badge severity; nothing else that
  // uses it changed.
  if (input.host.last_seen === null) {
    out.push({ severity: "critical", what: "never reported" });
  } else {
    const age = now.getTime() - new Date(input.host.last_seen).getTime();
    if (age > STALE_THRESHOLD_MS) {
      out.push({
        severity: "critical",
        what: `last reported ${relative(input.host.last_seen, now)}`,
      });
    }
  }

  for (const fs of input.filesystems) {
    // df's Use%: used / (used + free). total is not the denominator -- see
    // filesystemRows above. Used only to decide whether to warn; the card
    // itself still shows bytes.
    if (fs.used === null || fs.free === null) continue;
    const capacity = fs.used + fs.free;
    if (capacity === 0) continue;
    const full = (fs.used / capacity) * 100;
    if (full >= DISK_WARN_PCT) {
      out.push({
        severity: full >= DISK_CRIT_PCT ? "critical" : "warning",
        what: `${fs.label} is ${percent(full)} full — ${bytes(fs.free)} free`,
      });
    }
  }

  for (const unit of input.units ?? []) {
    if (unit.state === "failed") {
      out.push({ severity: "warning", what: `${unit.unit_name} failed` });
    } else if (flapping(unit)) {
      out.push({
        severity: "warning",
        what: `${unit.unit_name} restarted ${unit.restarts_1h} times in the last hour`,
      });
    }
  }

  return out;
}

/**
 * Whether a unit is stuck in a restart loop.
 *
 * Repetition is a RATE, so it is measured by counting transitions rather than
 * by inspecting the current state. Two tempting shortcuts are both wrong:
 *
 * - `substate === "auto-restart"` is one sighting, not a rate. It is also the
 *   gap BETWEEN attempts, which at the default RestartSec=100ms a 60-second
 *   scrape will essentially never land in.
 * - "how long has it been in auto-restart" is worse: `since` advances on every
 *   state CHANGE, and a flapping unit changes state constantly, so its age in
 *   the current state is near zero exactly when it is flapping hardest.
 *
 * The unit this catches is the one nothing else can: a service that runs for a
 * few minutes, dies, and comes back looks perfectly healthy at almost every
 * scrape, and systemd never escalates it to `failed` because it does not trip
 * the start limit. Only its history gives it away.
 */
function flapping(unit: Unit): boolean {
  return unit.restarts_1h >= FLAP_THRESHOLD;
}

/**
 * The reporting agent, identified exactly: its version and the commit it was
 * built from.
 *
 * buildinfo.Commit() is already the SHORT sha, so nothing is truncated here
 * -- and it is "unknown" for a binary built without the ldflags stamp, which
 * is a real state (a `go build` from a working tree) and not a value worth
 * printing. An unstamped build falls back to the version alone rather than
 * reading "0.4.1 · unknown", which looks like a bug in netra rather than a
 * fact about how that agent was compiled.
 *
 * A version with no commit at all is still the answer when that is all the
 * host sent; a host that reported neither reads as absent, never as an empty
 * string.
 */
function agentBuild(host: HostDetail): string {
  const version = host.agent_version;
  const commit = host.build_commit;
  if (version === null) return commit ?? ABSENT;
  if (commit === null || commit === "" || commit === "unknown") return version;
  return `${version} · ${commit}`;
}

/** A labelled landmark around a Card, so each summary is reachable by name
 * (Card itself renders a plain div and has no labelling of its own). */
function Panel({
  label,
  title,
  action,
  children,
}: {
  label: string;
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-label={label}>
      <Card title={title} action={action}>
        {children}
      </Card>
    </section>
  );
}

/**
 * One card's worth of sensor rows: name, recent history, current reading.
 *
 * A sensor reading is only interesting as a movement. One number says
 * 48 °C, or 1180 RPM, which a reader cannot judge without knowing whether
 * it has been there all day or has been climbing for an hour -- so every
 * sensor gets its history beside its value.
 *
 * Shared by the temperature, fan and power cards so the three read
 * identically; the kind still decides the unit, the precision and (for
 * fans) which aggregate is honest.
 */
function SensorList({
  res,
  rows,
  color,
  trend,
  empty,
  range,
  fetchFamily,
}: {
  res: MetricsResponse | null;
  rows: { series: MetricsSeries; index: number }[];
  color: string;
  /** The word used in each sparkline's accessible label. */
  trend: string;
  empty: string;
  range?: Range;
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}) {
  if (rows.length === 0) return <p className="note">{empty}</p>;
  return (
    <div className="sensor-list">
      {rows.map(({ series, index }) => {
        const kind = series.key.kind ?? "temperature";
        const name = sensorName(series);
        // Temperatures keep reading `temp`: it is the column they have
        // always been drawn from, it is the one every historical row was
        // written into, and an agent predating `value` filled only that.
        const history =
          kind === "temperature"
            ? griddedValues(res, index, "temp")
            : sensorHistory(res, index, kind);
        const value = lastReading(history);
        return (
          <div className="sensor-row" key={`${name}-${index}`}>
            <span className="lab">{name}</span>
            {/* Free-scaled to its own extent, deliberately: these sit in
                one list but a CPU package and an NVMe drive do not share a
                sensible axis, and a shared one would flatten every sensor
                against the largest. The question here is "is this one
                moving", not "which is biggest" -- and across kinds it is
                not even arithmetic: 1200 RPM and 12 V on one scale is a
                flat line at the bottom of the card. */}
            <Enlargeable
              title={`${name} · ${trend}`}
              label={`Enlarge ${trend} for ${name}`}
              className="inline"
              series={[{ name, color, values: history }]}
              // The window these readings were gridded against, so the
              // enlarged view carries a time axis from the moment it is
              // opened rather than only once a range has been changed.
              window={res?.window ?? null}
              // Free-scaled in the dialog too, and re-scaled after a range
              // change: the small chart above scales to its own extent, and a
              // chart that snapped to a zero floor on being enlarged would
              // draw a 44-47 degree package as a flat line.
              autoScale
              fmt={(n) => formatSensor(kind, n)}
              range={range}
              ranges={RANGE_VALUES}
              fetchSeries={
                fetchFamily === undefined
                  ? undefined
                  : async (next) => {
                      const answered = await fetchFamily("sensor", next);
                      // Re-found by its own key, never by the index it had
                      // in the previous response: a sensor that stopped
                      // reporting shifts every series after it, and the
                      // chart would silently become another sensor's.
                      //
                      // Kind included in the match, because the name is only
                      // chip+label: sensorsOfKind() picked these rows by kind
                      // and this search does not, so a fan and a temperature
                      // sharing a chip and a label would match each other --
                      // and a temperature chart reading `temp` off a fan
                      // series draws nothing at all. Two keyless series are
                      // both named ABSENT, which makes the kind the only
                      // thing separating them.
                      const at = answered.series.findIndex(
                        (s) =>
                          sensorName(s) === name &&
                          (s.key.kind ?? "temperature") === kind,
                      );
                      const values =
                        at === -1
                          ? []
                          : kind === "temperature"
                            ? griddedValues(answered, at, "temp")
                            : sensorHistory(answered, at, kind);
                      return {
                        series: [{ name, color, values }],
                        window: answered.window,
                      };
                    }
              }
            >
              <Sparkline
                values={history}
                width={110}
                height={24}
                color={color}
                label={`${name} ${trend} trend`}
              />
            </Enlargeable>
            <span className="val">{formatSensor(kind, value)}</span>
          </div>
        );
      })}
    </div>
  );
}

/** The same pairs as Facts, written label-above-value four across instead of
 * label-beside-value down a column -- the host's System card, which spans the
 * page and was spending 170px of the best position on it to say eight short
 * things.
 *
 * A key/value pair is still a dl: the wrapper div around each dt/dd is what
 * the HTML spec calls a name-value group, and it is a real grid item here
 * rather than the `display: contents` box that broke Facts' separators. No
 * nth-of-type selector depends on it.
 *
 * Values do not wrap. A processor model is the one string long enough to
 * need two lines, and letting it take them makes the block a different
 * height on every host in the fleet -- so it ellipsizes and carries the full
 * text as a title. See .sysstrip. */
function FactStrip({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="sysstrip">
      {rows.map(([key, value]) => (
        <div className="f" key={key}>
          <dt>{key}</dt>
          <dd title={typeof value === "string" ? value : undefined}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** `wide` lays the pairs out two across instead of one, for a card that spans
 * the page rather than half of it. See .kv.wide. */
function Facts({
  rows,
  wide = false,
}: {
  rows: [string, ReactNode][];
  wide?: boolean;
}) {
  return (
    <dl className={wide ? "kv wide" : "kv"}>
      {/* Fragment, not a `display: contents` div. The div was invisible in
        every sense but the one that mattered: display affects box generation,
        not selector matching, so each dt was the only dt under its own
        wrapper and `.kv dt:last-of-type` matched EVERY row -- no card built
        here drew the separators .kv specifies, while ContainerPage, which
        writes its dt/dd out by hand as direct children, drew them normally.
        The same class, two appearances, decided by which file you were in. */}
      {rows.map(([key, value]) => (
        <Fragment key={key}>
          <dt>{key}</dt>
          <dd>{value}</dd>
        </Fragment>
      ))}
    </dl>
  );
}

export interface OverviewProps {
  host: HostDetail;
  hostMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  agentMetrics: MetricsResponse | null;
  sensorMetrics: MetricsResponse | null;
  /** family=cpu_core for this host, one series per logical CPU. Absent on a
   * host too large to ask for them -- see MAX_PER_CORE in hostTrends.ts. */
  coreMetrics?: MetricsResponse | null;
  /** family=net for this host, one series per interface. */
  netMetrics?: MetricsResponse | null;
  containers: Container[] | null;
  units: Unit[] | null;
  /** The range this page is showing. Seeds the picker in every chart
   * enlarged out of this tab. */
  range?: Range;
  /** One family at one range, for an enlarged chart alone -- HostPage's
   * fetchFamily. Without it the enlarged charts carry no picker, which is
   * what a caller that cannot refetch one family should get. */
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
  /** Injected by tests so "last reported" is deterministic. */
  now?: Date;
}

export function Overview({
  host,
  hostMetrics,
  coreMetrics,
  netMetrics,
  filesystemMetrics,
  agentMetrics,
  sensorMetrics,
  containers,
  units,
  range,
  fetchFamily,
  now,
}: OverviewProps) {
  const perState = cpuStateBands(hostMetrics);

  // The headline chart is the per-core stack, the same one the fleet row for
  // this host draws -- the two must not disagree about the same machine.
  // Falling back to cpu_total when there are no per-core series: one true
  // band beats a not-collected panel where a silhouette is available.
  //
  // The user/system/iowait/steal breakdown is NOT the fallback any more. It
  // answers a different question -- where the time went, rather than which
  // core spent it -- so it has its own panel below rather than standing in
  // for this one.
  const total = griddedValues(hostMetrics, 0, "cpu_total");
  const perCore = perCoreBands(coreMetrics ?? null);
  const cpuBands: Band[] = perCore.length > 0 ? perCore : totalCpuBand(total);

  const memBands = memoryBands(hostMetrics);

  // A host's traffic is the sum over its interfaces, and a null anywhere in
  // a bucket makes that bucket's total unknowable rather than smaller --
  // counting it as zero would draw a dip that never happened. Same rule the
  // fleet row uses, so the two agree about one host.
  // Bucket PEAK rather than bucket mean -- see peakBase(). Same choice the
  // fleet row makes, including the per-interface summing caveat noted there,
  // so the two still agree about one host.
  const ingress = sumSeries(netMetrics, peakBase(netMetrics, "rx_bytes"));
  const egress = sumSeries(netMetrics, peakBase(netMetrics, "tx_bytes"));
  // The Traffic card's NUMBERS, as opposed to the series beside them: gauges
  // off host_current, blanked when the host is not reporting. isReporting is
  // the fleet list's predicate too, so a host cannot read offline there and
  // busy here.
  const live = isReporting(host, now);
  const currentRx = live ? host.net_rx_bytes : null;
  const currentTx = live ? host.net_tx_bytes : null;

  const memTotal = latest(hostMetrics, "mem_total") ?? host.memory_total;
  const memUsed = latest(hostMetrics, "mem_used") ?? host.mem_used;
  const swapTotal = latest(hostMetrics, "swap_total");
  const swapUsed = latest(hostMetrics, "swap_used");

  const filesystems = filesystemRows(filesystemMetrics);
  const attention = needsAttention({
    host,
    agentMetrics,
    hostMetrics,
    filesystems,
    units,
    now,
  });

  // Off the host row, not off `units`. The units endpoint returns only what
  // needs attention, so counting it would report "0 units" for a healthy host
  // running several hundred services -- and "1 units · 1 failed" for one with
  // a single broken service. These are the counts the agent reported.
  const servicesTotal = host.services_total ?? null;
  const failedUnits = host.services_failed ?? null;
  const capabilities = Object.entries(host.capabilities);

  // The sensor family carries fans, voltages, currents and power alongside
  // temperatures, and only temperatures have a `temp` column -- so mapping
  // the whole family into one panel headed "Temperature" filled it with
  // rows reading "nct6775 fan1 —", burying the readings it exists to show.
  //
  // Splitting by kind rather than discarding the rest is the point of the
  // change: a stopped fan and a sagging rail are the two hardware failures
  // a temperature cannot show, and each kind gets its own card so a
  // 1200 RPM fan never shares an axis with a 45 °C package.
  const temperatureSeries = sensorsOfKind(sensorMetrics, ["temperature"]);
  const fanSeries = sensorsOfKind(sensorMetrics, ["fan"]);
  // Voltage, current and power share a card: they are all "the power
  // delivery is or is not healthy", and on a typical board there are one or
  // two of each -- three separate cards of one row would be mostly heading.
  const powerSeries = sensorsOfKind(sensorMetrics, [
    "voltage",
    "current",
    "power",
  ]);

  // How close each kernel ceiling came to being hit.
  //
  // The gauge reads the PEAK bucket where the tier has one: at 5m a mean of
  // 40% descriptor use across five minutes can hide a moment at 99%, and
  // the moment is the whole question -- accept() fails at the peak, not at
  // the average. The suffixed name has to be spelled out because
  // candidates() prefers _avg. At the raw tier there is no _max peer and
  // the sample is itself the instant, so the bare name is exact.
  //
  // The ceiling is read as the last value the host ever reported rather
  // than the latest bucket: a configured kernel limit does not stop being
  // the limit because the newest bucket has not materialised yet. Same
  // reasoning Inventory applies to a container's mem_limit.
  const limitRows = LIMITS.map((limit) => {
    const peak = `${limit.gauge}_max`;
    const gauge = carriesColumn(hostMetrics, peak) ? peak : limit.gauge;
    const history = griddedValues(hostMetrics, 0, gauge);
    const capability = host.capabilities[limit.capability];
    return {
      ...limit,
      // "ok" is the agent saying the collector ran; only a real reason
      // stands in for a reading.
      reason:
        capability !== undefined && capability !== "ok" ? capability : null,
      value: latestValue(history),
      ceiling: latest(hostMetrics, limit.ceiling),
      carried: carriesColumn(hostMetrics, limit.gauge) && hasReading(history),
    };
  }).map((row) => ({
    ...row,
    // A ceiling so high it is not a ceiling.
    //
    // fd_limit is /proc/sys/fs/file-max, and a great many hosts set it to
    // int64 max as "no practical limit". Drawing 3352 against 9.2
    // quintillion is a bar that can never move, and the number beside it is
    // worse than useless: past Number.MAX_SAFE_INTEGER a JSON integer has
    // already lost precision by the time it reaches this line, so
    // 9223372036854775807 renders as ...776000 -- a figure the host never
    // reported. Say "no limit", which is both true and what the operator
    // means when they set it.
    unbounded: row.ceiling !== null && row.ceiling > Number.MAX_SAFE_INTEGER,
  }));

  // Read once and used twice -- the summary line and the strip's Uptime row.
  // Two copies of this expression is how the two come to disagree on a host
  // whose metric and whose host row say different things.
  const uptime = latest(hostMetrics, "uptime_s") ?? host.uptime_s;

  const limitsWorthShowing = limitRows.some(
    (row) => row.carried || row.reason !== null,
  );

  return (
    <>
      {/* Above the cards, not among them.
          .grid2 is a CSS multi-column flow, so a card placed first in it only
          reaches the top of the LEFT column -- what is wrong with the host sat
          eighth, below the disk meters, at the width of half the page. It is
          the first thing on this tab that must be read, so it is lifted out of
          the flow entirely and spans the page. The System card below it is
          hoisted the same way for a different reason -- see there.

          Present only when something is wrong, the same rule the fleet band
          follows: a permanently visible "All clear" box in the best position
          on the page is a box people stop reading, and it costs that position
          on every healthy host. When there is nothing to say, one quiet line
          says the check ran. */}
      {attention.length === 0 ? (
        <p className="allclear">Nothing needs attention on this host</p>
      ) : (
        <section className="attn" aria-label="Needs attention">
          <header>
            <h3>
              {attention.length} thing{attention.length === 1 ? "" : "s"} need
              {attention.length === 1 ? "s" : ""} attention
            </h3>
          </header>
          {/* The severity is a heading over the rows at that severity, said
              once, rather than a chip repeated on every one of them. Three
              critical rows do not need the word "critical" three times, and
              the chip that used to carry it printed the raw severity literal
              in lowercase. The dot on each row is the mark; the heading above
              it is the word §3.3 requires, and it is a real heading so a
              screen reader reaches the rows through it.

              The fleet list groups the same way, and this page has to match
              it -- see #92, where the two disagreed about one host. */}
          {ATTENTION_SEVERITIES.map((severity) => {
            // Stable partition, so the written order of needsAttention()
            // survives inside each severity: reporting still leads, which is
            // the whole reason it is written first.
            const rows = attention.filter((a) => a.severity === severity);
            if (rows.length === 0) return null;
            return (
              <div key={severity}>
                <h4 className={`attn-sev ${SEVERITY_CLASS[severity]}`}>
                  {SEVERITY_WORD[severity]}{" "}
                  <span className="n">{rows.length}</span>
                </h4>
                <ul className="attn-list">
                  {rows.map((a, index) => (
                    <li className="attn-row" key={index}>
                      <span
                        className={`dot ${SEVERITY_CLASS[severity]}`}
                        aria-hidden="true"
                      />
                      <span className="what">{a.what}</span>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </section>
      )}

      {/* Out of the flow, like the attention band -- but not because it has to
          be read first. It is the machine's identity card, and it was asked
          for at the top right of the page. .grid2 is a CSS multi-column flow,
          not a grid: cards run down the left column and then down the right,
          and the browser picks the break point from content height, so there
          is no top-right slot to reorder a card into. Full width above the
          flow is the position the mechanism can actually hold.

          Shut by default, and that is the change here. Writing the eight
          facts label-above-value four across took the block from ~170px to
          ~115px, which fixed the content and left the frame: a card header, a
          border, 16px of body padding top and bottom and a bottom margin --
          more chrome than the facts it wrapped, in the best position on the
          page, for identity that is read once when the page opens and then
          scrolled past. So the summary IS the card now, one line carrying the
          five facts read in passing, and the labelled eight-fact strip is
          what opens underneath. Nothing is dropped; three facts stop being
          permanently on screen.

          A native <details>, no mirrored useState -- the same shape the fleet
          attention band uses, and for the reason spelled out there: the
          element owns the open state, and the disclosed content has to live
          INSIDE it or a screen reader is told "expanded" and handed nothing.

          <section aria-label="System"> stays. With the card header gone the
          word "System" is painted nowhere, so the accessible name is the only
          thing left carrying it -- and it is what every test on this card
          finds the card through. See .sysfold. */}
      <section aria-label="System">
        <details className="sysfold">
          {/* Five facts, and an absent one is simply not written rather than
              given an em dash. That is the rule the Temperature card and the
              fleet list's site line already follow: a dash is a placeholder
              for a value that should be there and is missing, and a VPS that
              never reported a cpu_model is not missing anything. The labelled
              strip below keeps ABSENT, because a labelled table is where a
              gap does need a mark. */}
          <summary>
            {/* The distribution mark, in currentColor rather than in Ubuntu
                orange or Fedora blue -- see lib/osIcon.ts. It is inside the
                OS span so it cannot be separated from the name it labels by
                a wrap, and it is aria-hidden: the name is written next to
                it. A host whose os_name the table does not recognise gets no
                icon and no gap. */}
            {/* Guarded like the other four. os_name is nullable in the store
                -- ingest.go NULLIFs it, for an agent in a container that
                cannot read the host's /etc/os-release -- and osLabel(null) is
                ABSENT, so an unguarded span left a host with nothing else to
                say showing a summary line of one em dash. The separators are
                drawn from the SECOND span onward, so whichever fact ends up
                first simply takes no leading dot. */}
            {host.os_name !== null && (
              <span className="strong">
                <OsIcon name={host.os_name} />
                {osLabel(host.os_name)}
              </span>
            )}
            {host.kernel !== null && <span>{host.kernel}</span>}
            {/* The one value long enough to push this line onto a second row
                by itself: cpu_model is the raw string -- "AMD EPYC 7402P
                24-Core Processor", not the short marketing name. It gives way
                first and carries its full text as a title, the same treatment
                .sysstrip dd gives it for the same reason. */}
            {host.cpu_model !== null && (
              <span className="cpu" title={host.cpu_model}>
                {host.cpu_model}
              </span>
            )}
            {host.memory_total !== null && (
              <span>{binaryBytes(host.memory_total)}</span>
            )}
            {/* Guarded like the three above it, and for the same reason:
                uptime_s is nullable on both the metric and the host row, and
                duration(null) is ABSENT -- so an unguarded span writes
                "up —" on a host that never reported one, which is the exact
                placeholder this line does not write. */}
            {uptime !== null && (
              <span className="dim">up {duration(uptime)}</span>
            )}
            <span className="more">Details</span>
          </summary>
          <div className="body">
            <FactStrip
              rows={[
                ["OS", osLabel(host.os_name)],
                ["Kernel", host.kernel ?? ABSENT],
                ["Architecture", host.arch ?? ABSENT],
                ["Processor", host.cpu_model ?? ABSENT],
                [
                  "Cores",
                  host.cores === null
                    ? ABSENT
                    : `${host.cores} cores · ${host.threads ?? ABSENT} threads`,
                ],
                // The machine's installed RAM, which this page never stated.
                // memory_total was blank on every host until the agent started
                // sending it, and its only reader since has been the memory
                // chart's fallback denominator -- so the fact itself, the one
                // an operator asks for first when sizing anything, was
                // collected and never shown.
                ["Memory", binaryBytes(host.memory_total)],
                ["Uptime", duration(uptime)],
                // The exact binary that is reporting, not just its release.
                //
                // "0.4.1" does not identify a build: it is whatever was last
                // tagged, and the agent in front of you may be a rebuild, a
                // patched branch, or the same tag from before a fix landed. The
                // commit is what makes the answer exact, and it is the first
                // thing anyone asks when a host reports something the code is
                // not supposed to be able to report. Both were already collected
                // (buildinfo.Version and buildinfo.Commit) and served on
                // HostDetail; only the version was ever shown.
                ["Agent", agentBuild(host)],
              ]}
            />
          </div>
        </details>
      </section>

      <div className="grid2">
        {/* First card in the flow, so it takes the top of the LEFT column.
          In above the line, out below -- the same mark the fleet row
          draws, because a reader moving between them should not have to
          re-learn the chart. There was no traffic card on this page at all:
          the only network chart lived in the Graphs tab, so the overview
          summarised every subsystem except the one most likely to explain a
          problem. */}
        <Panel label="Traffic" title="Traffic">
          {ingress.length === 0 && egress.length === 0 ? (
            <p className="note">No interface samples in this window.</p>
          ) : (
            <div className="traffic-cell">
              <Enlargeable
                // "in" and "out", not rx and tx: the direction is the
                // point of this chart, and "rx" is the kernel's word for it
                // rather than the reader's. The wire and the schema keep
                // rx/tx.
                title="Traffic"
                // The same page the fleet row's traffic cell links to, for
                // the same reason: this is the summed chart, host-traffic
                // is the summed chart's spec, and the two pages must not
                // disagree about what clicking traffic does. `from` sends
                // Back here rather than to the Graphs tab.
                //
                // The dialog goes with the href (Enlargeable.tsx) -- it is
                // what a chart with no page gets, and this one has a page.
                href={
                  `/hosts/${encodeURIComponent(String(host.id))}/chart/host-traffic` +
                  `?from=overview` +
                  (range === undefined
                    ? ""
                    : `&range=${encodeURIComponent(range)}`)
                }
                // Inline, like every other sparkline this wraps: the chart
                // is a fixed 260px inside a flex column, and the full-width
                // default would spread the pressable area (and its zoom-in
                // cursor) across the empty half of the card.
                className="inline"
                unit="B/s"
                series={[
                  { name: "in", color: UP_COLOR, values: ingress },
                  { name: "out", color: DOWN_COLOR, values: egress },
                ]}
                mirrored
                fmt={bytes}
                window={netMetrics?.window ?? null}
                range={range}
              >
                <UpDownSparkline
                  up={ingress}
                  down={egress}
                  width={260}
                  height={64}
                  label="Traffic in and out over time"
                />
              </Enlargeable>
              <div className="traffic-rates">
                {/* byterate, never bitrate: net_rx/net_tx are BYTES per
                  second, so bitrate() rendered every host's traffic 8x low
                  and plausibly. The fleet's traffic cell carried the same
                  bug, so the two pages agreed with each other and with
                  nothing else.

                  The numbers are host_current's gauges, not the end of the
                  series drawn beside them. Off the series they moved with
                  the RANGE -- the raw instantaneous rate at 1h, a
                  five-minute average from a quarter of an hour ago at 6h and
                  wider -- so widening the window changed what "now" meant.
                  The sparkline still follows the range; the rates do not.

                  Gated on the host still reporting: the gauge is the one
                  number here that does not go absent by itself when the
                  agent dies -- host_current keeps the last pair it was
                  written -- and "the agent is down" must not render as
                  "traffic is steady". */}
                <span className="rate">↑ {byterate(currentRx)} in</span>
                <span className="rate">↓ {byterate(currentTx)} out</span>
              </div>
            </div>
          )}
        </Panel>

        {/* Overlay (inside ChartPanel) renders the full legend itself once a
          panel carries two or more bands, so none is built here. */}
        <section aria-label="Processor">
          {/* No unit prop: percent() prints one already, and passing both
            rendered "12 % %". See ChartPanel's unit prop. */}
          <ChartPanel
            title="Processor"
            series={cpuBands}
            // No ceiling for the per-core stack: the bands are each core's real
            // utilisation, so the stack runs to cores x 100 and the height is a
            // shape rather than a quantity. The cpu_total fallback is a
            // percentage of the host and keeps the 0-100 axis.
            max={perCore.length > 0 ? undefined : 100}
            hideAxis={perCore.length > 0}
            fmt={(n) => percent(n)}
            // The host's CPU, not core 0's.
            //
            // A panel with more than one band headlines series[0] and names
            // it, which is right for a Network panel -- "rx 1.2 MB/s" says
            // whose number that is. Here series[0] is literally the first
            // core, so the card read "core 0 6 %" beside a shape that is the
            // whole machine, and on anything above six cores the legend is
            // suppressed and nothing on the card said what "core 0" was.
            //
            // The stack cannot supply the number either: these bands are each
            // core's real utilisation, so the top of the stack is N x 100 and
            // not a percentage of anything. cpu_total is the mean across
            // cores, which is what a reader means by "the CPU".
            nowValue={perCore.length > 0 ? latestValue(total) : undefined}
            window={hostMetrics?.window ?? null}
            // Each core contributes busy/N, so the stack's top edge is the mean
            // across cores -- cpu_total -- and 100 stays the right ceiling
            // however many cores the host has.
            //
            // Stacked for the cpu_total fallback too, even though one band is
            // not much of a stack: the mark must not change depending on
            // whether a host happened to be small enough to ask for its cores,
            // or two machines side by side would look like different metrics.
            stacked={cpuBands.length > 0}
            // No legend once the bands are cores: thirty-two entries are
            // longer than the chart, and the enlarged view's table already
            // names every core beside its colour. This used to suppress the
            // legend by passing `highlight`, which ALSO dims every other series
            // to 35% -- the whole stack went pale to hide a list.
            legend={perCore.length <= 6}
            // An empty band list is a tier that does not carry the columns, and
            // an empty chart asserts the host reported nothing. Say which it is.
            unavailable={
              cpuBands.length === 0
                ? "The host reported no processor samples in this window."
                : undefined
            }
            range={range}
            ranges={RANGE_VALUES}
            // Whichever family this panel is actually drawing: the per-core
            // stack widens as cpu_core, the cpu_total fallback as host. Asking
            // for cpu_core on a host too large to have been given it would
            // answer with a chart the small panel never showed.
            fetchSeries={
              fetchFamily === undefined
                ? undefined
                : async (next) => {
                    if (perCore.length > 0) {
                      const answered = await fetchFamily("cpu_core", next);
                      const cores = perCoreBands(answered);
                      if (cores.length > 0) {
                        return { series: cores, window: answered.window };
                      }
                      // Falling back needs a SECOND request, not cpu_total
                      // out of this one: family=cpu_core reads
                      // cpu_core_samples, which has no cpu_total column, so
                      // asking this response for it can only ever answer
                      // with an empty array -- a blank dialog with nothing
                      // saying why. The window a host with no cores at this
                      // range still has a silhouette in is the host family's.
                    }
                    const answered = await fetchFamily("host", next);
                    return {
                      series: totalCpuBand(
                        griddedValues(answered, 0, "cpu_total"),
                      ),
                      window: answered.window,
                    };
                  }
            }
          />
        </section>

        {/* The breakdown keeps its own panel rather than being displaced by the
          per-core stack: "which core" and "doing what" are different
          questions and a reader wants both. It used to vanish above an hour
          because cpu_user/system/iowait/steal lived only in the raw table;
          they reach the 5m and 1h rollups now, so this survives the range
          control. */}
        <ChartPanel
          title="CPU time breakdown"
          series={perState}
          max={100}
          fmt={(n) => percent(n)}
          stacked
          window={hostMetrics?.window ?? null}
          unavailable={
            perState.length === 0
              ? "cpu_user, cpu_system, cpu_iowait and cpu_steal are not stored at this resolution."
              : undefined
          }
          range={range}
          ranges={RANGE_VALUES}
          fetchSeries={
            fetchFamily === undefined
              ? undefined
              : async (next) => {
                  const answered = await fetchFamily("host", next);
                  return {
                    series: cpuStateBands(answered),
                    window: answered.window,
                  };
                }
          }
        />

        {/* The stack and the meter answer different questions and both stay:
          the meter says how full the host is right now, the chart says how it
          got there. The ceiling is mem_total rather than the stack's own
          running total, so the gap at the top is free memory -- a stack
          scaled to itself always touches the top and would report every host
          as full. */}
        <ChartPanel
          title="Memory"
          series={memBands}
          // Headroom above total so the rule marking it reads as a rule rather
          // than as the top border of the plot.
          max={memTotal === null ? undefined : memTotal * 1.08}
          reference={memTotal ?? undefined}
          // Binary here too: the bands are read against the ceiling rule, and a
          // stack labelled decimally under a rule labelled binarily makes one
          // quantity look like two.
          fmt={(n) => binaryBytes(n)}
          // ...and the axis ticks on the same ladder the formatter prints on.
          // Ticked decimally, a 16 GiB host reads 1.9 / 3.7 / 5.6 GiB and
          // every label on the axis is a ragged number. This is the family
          // tickBase exists for.
          tickBase={1024}
          // The ceiling used to be drawn as text inside the plot, over the
          // rule. It belongs beside the reading it is a ceiling for: the
          // header already says how much is used, and the pair says of what.
          nowFmt={(n) => binaryBytesPair(n, memTotal)}
          stacked
          window={hostMetrics?.window ?? null}
          unavailable={
            memBands.length === 0
              ? "The host reported no memory samples in this window."
              : memTotal === null
                ? "The host's total memory is unknown, so there is no ceiling to draw the bands against."
                : undefined
          }
          range={range}
          ranges={RANGE_VALUES}
          fetchSeries={
            fetchFamily === undefined
              ? undefined
              : async (next) => {
                  const answered = await fetchFamily("host", next);
                  return {
                    series: memoryBands(answered),
                    window: answered.window,
                  };
                }
          }
        />

        <Panel label="Memory" title="Memory">
          <Meter
            label="used"
            value={memUsed}
            max={memTotal}
            formatValue={(value, max) => binaryBytesPair(value, max)}
          />
          {/* Three states, not two. swap_total lives only in the raw table
            (0001_init.sql) -- the 5m and 1h rollups do not carry it -- so at
            any range above an hour the value is missing because the TIER
            has no such column, not because the host has no swap. Collapsing
            those told a host with 8 GB of swap in use that it had none: an
            absent column rendered as a positive fact about the machine. */}
          {!carriesColumn(hostMetrics, "swap_total") ? (
            <div className="mrow">
              <div>
                <div className="lab">swap</div>
              </div>
              <div className="val">not at this resolution</div>
            </div>
          ) : swapTotal === null ? (
            // Meter's absent state renders the em-dash marker and its
            // noLimit state says "no limit" -- the container-limit wording.
            // Neither is the fact here, which is that this host has no swap
            // configured at all, so the row is written out in Meter's own
            // markup with the one word that is true.
            <div className="mrow">
              <div>
                <div className="lab">swap</div>
              </div>
              <div className="val">none</div>
            </div>
          ) : (
            <Meter
              label="swap"
              value={swapUsed}
              max={swapTotal}
              formatValue={(value, max) => binaryBytesPair(value, max)}
            />
          )}
        </Panel>

        <Panel label="Disk" title="Disk">
          {filesystems.length === 0 ? (
            <p className="note">No filesystem samples in this window.</p>
          ) : (
            <div className="fs-list">
              {filesystems.map((fs) => (
                <Meter
                  key={fs.label}
                  label={fs.label}
                  // df's Use%: used / (used + free), never used / total. total
                  // includes the root reserve, which is neither in use nor
                  // allocatable, so dividing by it reports a full disk as less
                  // full than df does -- and df's number is the one the
                  // operator has already seen over SSH. Same definition the
                  // fleet's disk column uses, so the two cannot disagree about
                  // one filesystem.
                  value={fs.used}
                  max={
                    fs.used === null || fs.free === null
                      ? null
                      : fs.used + fs.free
                  }
                  formatValue={() =>
                    `${bytes(fs.used)} used · ${bytes(fs.free)} free · ${bytes(fs.total)} size`
                  }
                />
              ))}
              <p className="note">
                Bytes as measured: used and free do not sum to size, and the
                difference is the root reserve.
              </p>
            </div>
          )}
        </Panel>

        <Panel label="Inventory" title="Inventory">
          <Facts
            rows={[
              [
                "Containers",
                containers === null
                  ? ABSENT
                  : `${containers.length} containers`,
              ],
              [
                "Units",
                servicesTotal === null
                  ? ABSENT
                  : `${servicesTotal} units · ${failedUnits ?? 0} failed`,
              ],
              [
                "Filesystems",
                filesystems.length === 0
                  ? ABSENT
                  : `${filesystems.length} mounted`,
              ],
            ]}
          />
        </Panel>

        {/* All three sensor cards are rendered only when the host actually
          reports that kind of reading: a VPS has no hwmon at all, and most
          VMs and every container host report no fans, so an empty card on
          every cloud instance in the fleet would teach people to skip the
          column this page is made of. Why it is emptiness in the WINDOW
          rather than the agent's `sensors: absent` capability: the three
          cards then say the same thing the same way, and the capability is
          still spelled out on the Collectors card below for anyone asking
          why the readings are gone. The cost is that a host which does have
          sensors but reported none in the selected range loses the card
          instead of showing the sentence -- which is also why the `empty`
          strings below can no longer render, and are kept only so a future
          caller of SensorList inherits the wording. */}
        {/* Temperature is --s1, not the --s7 orange it used to be. Orange was
            chosen because temperature reads as heat, and that is exactly the
            problem: --s7 sits a few degrees from --accent and --st-serious, so
            a CPU at a perfectly normal 46 degrees drew itself in the colour
            this app uses for "look at this". A sensor list states a reading;
            it does not rank it. --s1 is the single-series default (Sparkline),
            which is what each row here is. */}
        {temperatureSeries.length > 0 && (
          <Panel label="Temperature" title="Temperature">
            <SensorList
              res={sensorMetrics}
              rows={temperatureSeries}
              range={range}
              fetchFamily={fetchFamily}
              color="var(--s1)"
              trend="temperature"
              empty="No temperature readings in this window."
            />
          </Panel>
        )}

        {fanSeries.length > 0 && (
          <Panel label="Fans" title="Fans">
            <SensorList
              res={sensorMetrics}
              rows={fanSeries}
              range={range}
              fetchFamily={fetchFamily}
              color="var(--s3)"
              trend="speed"
              empty="No fan readings in this window."
            />
          </Panel>
        )}

        {powerSeries.length > 0 && (
          <Panel label="Power" title="Power">
            <SensorList
              res={sensorMetrics}
              rows={powerSeries}
              range={range}
              fetchFamily={fetchFamily}
              color="var(--s5)"
              trend="reading"
              empty="No power readings in this window."
            />
          </Panel>
        )}

        {/* Headroom against the kernel's own ceilings. Nothing else on this
          page answers "am I about to hit a limit": a host at 98% of its
          conntrack table has a perfectly calm CPU, memory and disk card,
          and the first symptom is a network that appears broken. */}
        {limitsWorthShowing && (
          <Panel label="Limits" title="Limits">
            {limitRows.map((row) =>
              row.reason !== null ? (
                // The capability the agent reported, in place of the meter it
                // explains. "conntrack: unavailable" is an answer; an
                // em-dash next to a bar that never fills is a mystery, and
                // the reader cannot tell it from a collector that broke.
                //
                // Written out in Meter's own markup rather than passed INTO
                // Meter: that component backs the memory and disk cards too,
                // and its absent state is deliberately one thing. Same shape
                // the swap row above uses for the same reason.
                <div className="mrow" key={row.label}>
                  <div>
                    <div className="lab">{row.label}</div>
                  </div>
                  <div className="val">{row.reason}</div>
                </div>
              ) : row.unbounded ? (
                // The count still matters -- it is the only figure here --
                // but there is no ratio to draw it against.
                <div className="mrow" key={row.label}>
                  <div>
                    <div className="lab">{row.label}</div>
                  </div>
                  <div className="val">
                    {/* A no-break space inside "no limit": the value column
                      is narrow, and the default break put "no" on one line
                      and "limit" on the next. It wraps after the separator
                      instead. */}
                    {row.value === null
                      ? ABSENT
                      : `${cardinal(row.value)} · no limit`}
                  </div>
                </div>
              ) : (
                <Meter
                  key={row.label}
                  label={row.label}
                  value={row.value}
                  max={row.ceiling}
                  formatValue={(value, max) =>
                    `${cardinal(value)} of ${cardinal(max)}`
                  }
                />
              ),
            )}
          </Panel>
        )}

        <Panel label="Collectors" title="Collectors">
          {capabilities.length === 0 ? (
            <p className="note">The agent reported no capabilities.</p>
          ) : (
            <Facts
              rows={capabilities.map(([name, state]) => [
                name,
                // The reason is the value the agent sent; a collector that
                // cannot run says why rather than showing an empty chart.
                state === "ok" ? (
                  <Badge severity="ok">ok</Badge>
                ) : (
                  <span>{state}</span>
                ),
              ])}
            />
          )}
        </Panel>
      </div>
    </>
  );
}
