// The Temperature, Fan and Power cards, and the sensor-family helpers behind
// them.
//
// They lived on the Overview tab, where they were the last three cards in the
// last column -- three cards of hardware readings under four of software
// ones, on a page most hosts in a fleet render empty because a VPS has no
// hwmon at all. What a chip is measuring is a System fact, and System is
// where the CPU, memory and kernel panels already are.
//
// Moved whole rather than reimplemented: every comment below was bought by a
// specific failure -- a fan's minimum hiding a stall, four drivetemp chips
// all named "drivetemp temp1", a temperature drawn in the colour the app uses
// for "look at this" -- and re-deriving any of it is how those come back.
import { Fragment } from "react";
import type { MetricsResponse, MetricsSeries } from "../../../lib/api";
import { carriesColumn, griddedValues } from "../../../lib/metrics";
import { ABSENT } from "../../../lib/format";
import { Panel } from "./Panel";
import { Enlargeable } from "../../../ui/charts/Enlargeable";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { RAIL_RANGES } from "../../../lib/range";
import type { Range } from "../../../lib/range";

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

/** What a reader calls this sensor: chip and label, then the block device
 * where there is one.
 *
 * The instance is what tells four disks apart. The drivetemp driver names
 * every chip it registers "drivetemp" and publishes no tempN_label, so a
 * four-disk host draws four rows that would otherwise all read
 * "drivetemp temp1" -- four identical names for four different disks, and no
 * way to tell which one is the hot one. Empty for every chip that measures no
 * disk, which is every board sensor, so coretemp and the fans read exactly as
 * they always have.
 *
 * Also its identity across two responses -- the enlarged view re-finds its
 * own series by this name after a range change rather than by the index it
 * had, because a sensor that stopped reporting shifts every series after it
 * and the chart would silently become another sensor's. */
function sensorName(series: MetricsSeries): string {
  return (
    [series.key.chip, series.key.label, series.key.instance]
      .filter(Boolean)
      .join(" ") || ABSENT
  );
}

/** What the TILE calls this sensor: the chip is already the group heading
 * above it, so repeating it in every tile is what made the old rows too wide
 * to sit side by side -- "coretemp Core 7" needs twice the width of "Core 7"
 * and says nothing more once the heading reads "coretemp".
 *
 * The instance REPLACES a bare tempN rather than joining it. drivetemp
 * publishes no tempN_label (see sensorName above), so joining would print
 * four tiles reading "temp1 sda", "temp1 sdb" -- the disk, which is the whole
 * identity, buried behind a label that is identical on all four. A chip that
 * publishes a real label beside an instance keeps both, which is nvme:
 * "Composite nvme0n1" is what tells two controllers apart.
 *
 * Falls back to the chip before ABSENT, so a chip with one unlabelled input
 * renders as itself rather than as a nameless tile.
 */
function tileName(series: MetricsSeries): string {
  const label = series.key.label ?? "";
  const instance = series.key.instance ?? "";
  if (instance !== "" && (label === "" || /^temp\d+$/.test(label))) {
    return instance;
  }
  return (
    [label, instance].filter(Boolean).join(" ") || series.key.chip || ABSENT
  );
}

/**
 * The card's rows bucketed under the chip that produced them, chips and
 * members both in the order the response listed them.
 *
 * The carried `index` passes through untouched. It is the position in the
 * UNFILTERED res.series that griddedValues() reads by, and re-deriving it
 * from a group's own array is how one core ends up drawing another's history
 * -- see sensorsOfKind above, which is where the index comes from and why.
 */
function groupByChip(rows: { series: MetricsSeries; index: number }[]): {
  chip: string;
  members: { series: MetricsSeries; index: number }[];
}[] {
  const order: string[] = [];
  const byChip = new Map<string, { series: MetricsSeries; index: number }[]>();
  for (const row of rows) {
    const chip = row.series.key.chip ?? "";
    const members = byChip.get(chip);
    if (members === undefined) {
      byChip.set(chip, [row]);
      order.push(chip);
    } else {
      members.push(row);
    }
  }
  return order.map((chip) => ({ chip, members: byChip.get(chip) ?? [] }));
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

/** The digits alone, at the precision the kind deserves. Split from the unit
 * so a group heading can print "42-49 °C" rather than "42 °C-49 °C", which is
 * the same unit said twice around a dash. */
function sensorDigits(kind: string, value: number): string {
  switch (kind) {
    case "fan":
      return `${Math.round(value)}`;
    case "voltage":
    case "current":
      return value.toFixed(2);
    case "power":
      return value.toFixed(1);
    default:
      return `${Math.round(value)}`;
  }
}

/** Each kind in its own unit, because the reading is meaningless without
 * it and because these share a card. Precision differs by kind: a rail at
 * 11.9 V and one at 12.0 V are different facts, while a fan at 1183 and one
 * at 1180 RPM are the same fact. */
function sensorUnit(kind: string): string {
  switch (kind) {
    case "fan":
      return "RPM";
    case "voltage":
      return "V";
    case "current":
      return "A";
    case "power":
      return "W";
    default:
      return "°C";
  }
}

function formatSensor(kind: string, value: number | null): string {
  if (value === null) return ABSENT;
  return `${sensorDigits(kind, value)} ${sensorUnit(kind)}`;
}

/** Above this many readings, a card takes the whole .sm-sensors grid rather
 * than one of its tracks. A Raspberry Pi reports two temperatures and would
 * look abandoned spanning a 1400px page; a bare-metal box reports twenty-three
 * and cannot tile them inside one 420px track. */
const SPAN_CARD_MIN = 7;

/** The kinds whose readings sit on one number line, so a group's min and max
 * are a fact about the group rather than two unrelated nominals subtracted.
 * See groupSummary. */
const SPREAD_KINDS = new Set(["temperature", "fan"]);

/** The most .sensor-groups tracks one chip may claim -- eight 132px tracks is
 * about a full-width card on a laptop. A span wider than the grid is clamped
 * to it, so this is a ceiling on a wide page and nothing at all on a narrow
 * one. */
const GROUP_SPAN_MAX = 8;

/**
 * One reading's tile: what it is, what it says now, and where it has been.
 *
 * A sensor reading is only interesting as a movement. One number says
 * 48 °C, or 1180 RPM, which a reader cannot judge without knowing whether it
 * has been there all day or has been climbing for an hour -- so every tile
 * carries its history, seventeen cores included.
 *
 * The whole tile is the enlarge button: the reading and the name are as much
 * a part of "show me this sensor" as the chart is, and a 64px hit target
 * inside a 124px tile is a target people miss.
 */
function SensorTile({
  res,
  entry,
  color,
  trend,
  range,
  fetchFamily,
}: {
  res: MetricsResponse | null;
  entry: DrawnSensor;
  color: string;
  trend: string;
  range?: Range;
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}) {
  const { name, tile, kind, history, value } = entry;
  return (
    /* Free-scaled to its own extent, deliberately: these sit in one card but
       a CPU package and an NVMe drive do not share a sensible axis, and a
       shared one would flatten every sensor against the largest. The question
       here is "is this one moving", not "which is biggest" -- and across kinds
       it is not even arithmetic: 1200 RPM and 12 V on one scale is a flat line
       at the bottom of the card. */
    <Enlargeable
      title={`${name} · ${trend}`}
      label={`Enlarge ${trend} for ${name}`}
      className="sensor-tile"
      series={[{ name, color, values: history }]}
      // The window these readings were gridded against, so the enlarged view
      // carries a time axis from the moment it is opened rather than only
      // once a range has been changed.
      window={res?.window ?? null}
      // Free-scaled in the dialog too, and re-scaled after a range change:
      // the small chart above scales to its own extent, and a chart that
      // snapped to a zero floor on being enlarged would draw a 44-47 degree
      // package as a flat line.
      autoScale
      // Filled, like the Sparkline below it. The sparkline has always drawn
      // an area and the dialog drew a bare line, so opening a 44-47 degree
      // package swapped a shaded band for a hairline -- the enlarged view
      // saying less than the tile it came from. Honest here because the chart
      // free-scales: the fill's bottom edge is the quietest reading in the
      // window, not an axis decision.
      filled
      fmt={(n) => formatSensor(kind, n)}
      range={range}
      ranges={RAIL_RANGES}
      fetchSeries={
        fetchFamily === undefined
          ? undefined
          : async (next) => {
              const answered = await fetchFamily("sensor", next);
              // Re-found by its own key, never by the index it had in the
              // previous response: a sensor that stopped reporting shifts
              // every series after it, and the chart would silently become
              // another sensor's.
              //
              // sensorName, not tileName: the tile drops the chip, so two
              // chips that both publish "temp1" are one name here. Kind is in
              // the match for the same reason -- sensorsOfKind() picked these
              // rows by kind and this search does not, so a fan and a
              // temperature sharing a chip and a label would match each
              // other, and a temperature chart reading `temp` off a fan
              // series draws nothing at all. Two keyless series are both
              // named ABSENT, which makes the kind the only thing separating
              // them.
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
      <span className="lab" title={name}>
        {tile}
      </span>
      <span className="val">{formatSensor(kind, value)}</span>
      <Sparkline
        values={history}
        width={64}
        height={16}
        color={color}
        label={`${name} ${trend} trend`}
      />
    </Enlargeable>
  );
}

/** One sensor, with everything both the tile and its group heading need read
 * off the SAME history array -- so the digits, the line and the heading's
 * spread can never disagree. A fan reading 1180 RPM beside a line touching
 * zero is the exact confusion these cards exist to remove. */
interface DrawnSensor {
  name: string;
  tile: string;
  kind: string;
  history: (number | null)[];
  value: number | null;
}

function drawnSensor(
  res: MetricsResponse | null,
  { series, index }: { series: MetricsSeries; index: number },
): DrawnSensor {
  const kind = series.key.kind ?? "temperature";
  // Temperatures keep reading `temp`: it is the column they have always been
  // drawn from, it is the one every historical row was written into, and an
  // agent predating `value` filled only that.
  const history =
    kind === "temperature"
      ? griddedValues(res, index, "temp")
      : sensorHistory(res, index, kind);
  return {
    name: sensorName(series),
    tile: tileName(series),
    kind,
    history,
    value: lastReading(history),
  };
}

/**
 * What a group heading says beside the chip name.
 *
 * The spread, because that is the question a wall of tiles raises and cannot
 * answer at a glance: sixteen cores between 42 and 49 degrees is one fact,
 * and the reader who wanted it does not have to scan sixteen numbers for the
 * ends. On fans it is the one that catches a stall: 0-1574 RPM says a fan
 * stopped without the reader finding which tile reads zero.
 *
 * Temperatures and fans only. Every member sharing a kind is not enough --
 * a board's rails are all voltages and "1.16-11.97 V" is Vcore against +12V,
 * two nominals that were never on one number line, so the spread of a healthy
 * board reads like a fault. Watts against amps, on power_meter, are not even
 * the same quantity.
 *
 * The count only once a group is big enough that its tiles wrap, since below
 * that the reader can see how many there are.
 */
function groupSummary(members: DrawnSensor[]): string {
  if (members.length < 2) return "";
  const parts: string[] = [];
  if (members.length >= 5) parts.push(`${members.length} sensors`);
  const kinds = new Set(members.map((m) => m.kind));
  const values = members
    .map((m) => m.value)
    .filter((v): v is number => v !== null);
  const spreadable = kinds.size === 1 && SPREAD_KINDS.has(members[0].kind);
  if (spreadable && values.length > 0) {
    const kind = members[0].kind;
    const low = Math.min(...values);
    const high = Math.max(...values);
    parts.push(
      low === high
        ? formatSensor(kind, low)
        : `${sensorDigits(kind, low)}–${formatSensor(kind, high)}`,
    );
  }
  return parts.join(" · ");
}

/**
 * One card's worth of sensors: tiles, grouped under the chip that produced
 * them.
 *
 * Grouped rather than listed because the collector reports every labelled
 * hwmon input, and on a desktop CPU that is seventeen coretemp readings. As
 * one row each -- name, chart, value across a 292px column -- that card was
 * nine hundred pixels of System tab beside a three-row Fans card, and
 * sixteen lines of it read "coretemp Core N". The chip on the heading is
 * what pays for the tile: with it gone from every name, a tile fits in a
 * fifth of the width and the same readings tile into a fraction of the
 * height. Nothing is summarised away and nothing folds -- all seventeen are
 * on the page, each with its own history and its own enlarged view.
 *
 * Shared by the temperature, fan and power cards so the three read
 * identically; the kind still decides the unit, the precision and (for fans)
 * which aggregate is honest.
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
    /* ONE grid for the whole card, with the group headings spanning it,
       rather than a grid per group inside a grid of groups. Nested auto-fill
       grids resolve their tracks independently, so a chip with four readings
       drew tiles twice the width of the chip with seventeen -- the same card
       in two tile sizes, which reads as a mistake. */
    <div className="sensor-tiles">
      {groupByChip(rows).map(({ chip, members }) => {
        const drawn = members.map((row) => drawnSensor(res, row));
        const summary = groupSummary(drawn);
        return (
          <Fragment key={chip}>
            {/* A heading even for a chip that produced one reading: it is the
                other half of the tile's name, and dropping it on the singles
                would leave "temp1" standing alone with no way to know whose
                temp1 it is. */}
            <div className="ghead">
              <span className="gn">{chip || ABSENT}</span>
              <span className="rule" />
              {summary !== "" && <span className="gsum">{summary}</span>}
            </div>
            {drawn.map((entry, at) => (
              <SensorTile
                key={`${entry.name}-${at}`}
                res={res}
                entry={entry}
                color={color}
                trend={trend}
                range={range}
                fetchFamily={fetchFamily}
              />
            ))}
          </Fragment>
        );
      })}
    </div>
  );
}

/** Whether a card takes the whole .sm grid rather than one of its columns.
 * Undefined rather than an empty string so a card that does not span carries
 * no className at all, which is what it carried before. */
function spanClass(count: number): string | undefined {
  return count >= SPAN_CARD_MIN ? "sm-span" : undefined;
}

export interface SensorsProps {
  /** family=sensor for this host: temperatures, fans, voltages, currents and
   * power, one series per chip+label. */
  sensorMetrics: MetricsResponse | null;
  range?: Range;
  fetchFamily?: (family: string, range: Range) => Promise<MetricsResponse>;
}

/**
 * The three sensor cards, or nothing at all.
 *
 * Each card renders only when the host actually reports that kind of reading:
 * a VPS has no hwmon, and most VMs and every container host report no fans,
 * so an empty card on every cloud instance in the fleet would teach people to
 * skip the section it exists to show. Why it is emptiness in the WINDOW
 * rather than the agent's `sensors: absent` capability: the three cards then
 * say the same thing the same way, and the capability is still spelled out on
 * the Collectors tab for anyone asking why the readings are gone. The cost is
 * that a host which does have sensors but reported none in the selected range
 * loses the card instead of showing the sentence -- which is also why the
 * `empty` strings below can no longer render, and are kept only so a future
 * caller of SensorList inherits the wording.
 *
 * Voltage, current and power share a card: they are all "the power delivery
 * is or is not healthy", and on a typical board there are one or two of each
 * -- three separate cards of one row would be mostly heading.
 *
 * Nothing at all when the host reports no sensors of any kind, rather than an
 * empty heading: this sits between SystemGraphs and the Limits card, and a
 * "Sensors" heading over nothing would read as a section that failed to load.
 */
export function Sensors({ sensorMetrics, range, fetchFamily }: SensorsProps) {
  const temperatureSeries = sensorsOfKind(sensorMetrics, ["temperature"]);
  const fanSeries = sensorsOfKind(sensorMetrics, ["fan"]);
  const powerSeries = sensorsOfKind(sensorMetrics, [
    "voltage",
    "current",
    "power",
  ]);

  if (
    temperatureSeries.length === 0 &&
    fanSeries.length === 0 &&
    powerSeries.length === 0
  ) {
    return null;
  }

  return (
    <>
      {/* The same heading the chart groups above it carry, so the tab reads
          as one list of sections rather than as panels followed by
          something else. */}
      <h3 className="grouphead">Sensors</h3>
      <div className="sm sm-sensors">
        {temperatureSeries.length > 0 && (
          <Panel
            label="Temperature"
            title="Temperature"
            className={spanClass(temperatureSeries.length)}
          >
            {/* Temperature is --s1, not the --s7 orange it used to be. Orange
                was chosen because temperature reads as heat, and that is
                exactly the problem: --s7 sits a few degrees from --accent and
                --st-serious, so a CPU at a perfectly normal 46 degrees drew
                itself in the colour this app uses for "look at this". A
                sensor list states a reading; it does not rank it. --s1 is the
                single-series default (Sparkline), which is what each row here
                is. */}
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
          <Panel
            label="Fans"
            title="Fans"
            className={spanClass(fanSeries.length)}
          >
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
          <Panel
            label="Power"
            title="Power"
            className={spanClass(powerSeries.length)}
          >
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
      </div>
    </>
  );
}
