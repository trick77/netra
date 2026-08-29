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
              // Filled, like the Sparkline below it. The sparkline has always
              // drawn an area and the dialog drew a bare line, so opening a
              // 44-47 degree package swapped a shaded band for a hairline --
              // the enlarged view saying less than the 110px chart it came
              // from. Honest here because the chart free-scales: the fill's
              // bottom edge is the quietest reading in the window, not an
              // axis decision.
              filled
              fmt={(n) => formatSensor(kind, n)}
              range={range}
              ranges={RAIL_RANGES}
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
      <div className="sm">
        {temperatureSeries.length > 0 && (
          <Panel label="Temperature" title="Temperature">
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
      </div>
    </>
  );
}
