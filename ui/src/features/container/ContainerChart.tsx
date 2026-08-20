// A container list's CPU or Memory sparkline, with the enlarged view behind
// it.
//
// One component for both lists deliberately: the fleet's container overview
// and the host page's Containers tab are the same row (spec 4.5), and a
// chart that opened a different window depending on which list it was
// clicked in would be two different answers to one question.
import { Enlargeable, type DetailData } from "../../ui/charts/Enlargeable";
import { Sparkline } from "../../ui/charts/Sparkline";
import { bytes, percent } from "../../lib/format";
import { rangeLabel, type Range } from "../../lib/range";
import { containerTrends, fetchHostFamily } from "../fleet/hostTrends";
import type { ContainerRow } from "./columns";

export type ContainerMetric = "cpu" | "mem";

const SPEC: Record<
  ContainerMetric,
  { title: string; color: string; fmt: (n: number | null) => string }
> = {
  cpu: { title: "CPU", color: "var(--s1)", fmt: (n) => percent(n) },
  mem: { title: "Memory", color: "var(--s2)", fmt: bytes },
};

export interface ContainerChartProps {
  /**
   * The row this cell is in. It carries everything the enlarged view needs to
   * ask for its own data again: `host_id` because family=container is
   * per-host, and `container_key` because that is the stable identity -- two
   * containers can share a name across hosts, and only the key selects the
   * right series out of the response.
   */
  row: ContainerRow;
  metric: ContainerMetric;
  values: (number | null)[];
  /** The list's shared ceiling, so the column can be read down. Passed on to
   * the enlarged view too: a chart that rescaled itself on opening would
   * redraw the shape the reader clicked. */
  max: number;
  range: Range;
  /** The window the list was answered for, for the enlarged view's time
   * axis. Absent, no time axis is drawn rather than a guessed one. */
  window?: { from: string; to: string } | null;
  /** The ranges the PAGE offers. The dialog must not ask for a window its
   * own page could not express. */
  ranges?: readonly Range[];
}

export function ContainerChart({
  row,
  metric,
  values,
  max,
  range,
  window: answered = null,
  ranges,
}: ContainerChartProps) {
  const spec = SPEC[metric];
  // What a reader calls it, for the accessible name: twenty rows of
  // "Enlarge CPU" name twenty different charts identically. The key is the
  // fallback for the same reason the name cell falls back to it.
  const name = row.name ?? row.container_key;

  // family=container carries every container on the host, so widening one
  // row's chart costs the host's containers once -- there is no per-container
  // read route to ask more narrowly. The series for THIS row is then picked
  // by key, the same way the list itself picks it.
  const fetchSeries = async (next: Range): Promise<DetailData> => {
    const res = await fetchHostFamily(row.host_id, "container", next);
    const trend = containerTrends(res).get(row.container_key);
    return {
      // An empty band rather than none: the container existing in the list
      // and not in the widened window is a real answer ("it was not running
      // then"), and it draws as a gap rather than as a chart that failed.
      series: [
        {
          name: spec.title,
          color: spec.color,
          values: trend?.[metric] ?? [],
        },
      ],
      window: res.window,
    };
  };

  return (
    <Enlargeable
      title={`${spec.title} · ${name}`}
      // The metric's own title, verbatim: lowercasing it turned CPU into
      // "cpu", which a screen reader may attempt as a word rather than
      // spelling out. Every metric-named chart in the app reads the same way.
      label={`Enlarge ${spec.title} for ${name}`}
      className="inline"
      series={[{ name: spec.title, color: spec.color, values }]}
      max={max}
      fmt={spec.fmt}
      window={answered}
      range={range}
      ranges={ranges}
      fetchSeries={fetchSeries}
    >
      <Sparkline
        values={values}
        min={0}
        max={max}
        color={spec.color}
        label={`${spec.title} trend, ${rangeLabel(range)}`}
      />
    </Enlargeable>
  );
}
