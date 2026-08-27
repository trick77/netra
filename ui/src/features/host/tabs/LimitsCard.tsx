// Headroom against the kernel's own ceilings.
//
// It was a card on the Overview tab, among the cards that answer "how is this
// box doing". It is not that kind of fact: nothing here moves on a healthy
// host, and a reader who opens Overview is asking what changed. It is a fact
// about how the machine is configured and how close to those limits it runs,
// which is what the System tab is -- at the foot of it, under the charts the
// reader actually came for.
//
// Nothing else in netra answers "am I about to hit a limit": a host at 98% of
// its conntrack table has a perfectly calm CPU, memory and disk card, and the
// first symptom is a network that appears broken.
import type { HostDetail, MetricsResponse } from "../../../lib/api";
import {
  carriesColumn,
  griddedValues,
  hasReading,
  latestValue,
  optionalValues,
} from "../../../lib/metrics";
import { ABSENT, cardinal } from "../../../lib/format";
import { Meter } from "../../../ui/Meter";
import { Panel } from "./Panel";

/** The most recent non-null reading, or null when there is none. A null here
 * means "the host reported nothing", which the card renders as a word rather
 * than as 0.
 *
 * The same eight lines Overview.tsx and Graphs.tsx each carry, and copied for
 * the reason stated there: column() throws for a column the answering tier
 * does not have, so every lookup on a host page is optional by construction
 * or one absent column blanks the tab. */
function latest(res: MetricsResponse | null, base: string): number | null {
  const vals = optionalValues(res, 0, base);
  for (let i = vals.length - 1; i >= 0; i--) {
    const v = vals[i];
    if (v !== null) return v;
  }
  return null;
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

export interface LimitsCardProps {
  host: HostDetail;
  hostMetrics: MetricsResponse | null;
}

export function LimitsCard({ host, hostMetrics }: LimitsCardProps) {
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
  const rows = LIMITS.map((limit) => {
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

  // A host whose agent reports none of these -- an old build, a container
  // that cannot read /proc/sys -- gets no card at all rather than four empty
  // meters.
  const worthShowing = rows.some((row) => row.carried || row.reason !== null);
  if (!worthShowing) return null;

  return (
    <Panel label="Limits" title="Limits" className="limits">
      {rows.map((row) =>
        row.reason !== null ? (
          // The capability the agent reported, in place of the meter it
          // explains. "conntrack: unavailable" is an answer; an em-dash next
          // to a bar that never fills is a mystery, and the reader cannot
          // tell it from a collector that broke.
          //
          // Written out in Meter's own markup rather than passed INTO Meter:
          // that component backs the memory and disk cards too, and its
          // absent state is deliberately one thing.
          <div className="mrow" key={row.label}>
            <div>
              <div className="lab">{row.label}</div>
            </div>
            <div className="val">{row.reason}</div>
          </div>
        ) : row.unbounded ? (
          // The count still matters -- it is the only figure here -- but
          // there is no ratio to draw it against.
          <div className="mrow" key={row.label}>
            <div>
              <div className="lab">{row.label}</div>
            </div>
            <div className="val">
              {/* A no-break space inside "no limit": the value column is
                  narrow, and the default break put "no" on one line and
                  "limit" on the next. It wraps after the separator
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
  );
}
