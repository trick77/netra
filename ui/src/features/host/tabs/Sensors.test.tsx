import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { MetricsResponse } from "../../../lib/api";
import { Sensors } from "./Sensors";

// These moved here with the cards themselves. They were written against the
// Overview, which drew Temperature, Fans and Power until the tiles took that
// tab over; the assertions are unchanged because the component is -- what a
// chip is measuring is a System fact, and only the tab it renders on moved.
function response(
  over: Partial<MetricsResponse> & { family: string },
): MetricsResponse {
  return {
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  } as MetricsResponse;
}

function renderSensors(sensorMetrics: MetricsResponse | null) {
  return render(<Sensors sensorMetrics={sensorMetrics} />);
}

describe("Sensors", () => {
  // The whole section, not one card: a VPS has no hwmon at all, and a
  // "Sensors" heading over nothing reads as a section that failed to load.
  it("renders nothing at all for a host with no sensors", () => {
    const { container } = renderSensors(null);
    expect(container.firstChild).toBeNull();
  });

  it("renders nothing for a host whose sensor family came back empty", () => {
    const { container } = renderSensors(response({ family: "sensor" }));
    expect(container.firstChild).toBeNull();
  });

  it("reads the sensor family's temp column", () => {
    renderSensors(
      response({
        family: "sensor",
        // The window has to contain the point. Every sensor row now reads
        // its number off the SAME gridded array its sparkline draws, so a
        // fixture whose sample sits outside its own window grids to all
        // null and the row correctly reads absent -- which is what these
        // fixtures used to hide by taking the number from the ungridded
        // series while the sparkline beside it drew nothing.
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label"],
        columns: ["temp"],
        series: [
          {
            key: { chip: "coretemp", label: "Package id 0" },
            points: [[1_754_784_000_000, 47.5]],
          },
        ],
      }),
    );
    const temperature = screen.getByRole("region", { name: /temperature/i });
    // The chip names the group, the tile names the reading within it. The
    // full "coretemp Package id 0" survives on the enlarge button, which is
    // what the enlarged view is titled and what its refetch matches on.
    expect(within(temperature).getByText("coretemp")).toBeInTheDocument();
    expect(within(temperature).getByText("Package id 0")).toBeInTheDocument();
    expect(
      within(temperature).getByRole("button", {
        name: "Enlarge temperature for coretemp Package id 0",
      }),
    ).toBeInTheDocument();
    expect(within(temperature).getByText("48 °C")).toBeInTheDocument();
  });

  // The sensor family carries fans, voltages, currents and power now, and
  // only temperatures have a temp column. Mapping the whole family would fill
  // a panel headed "Temperature" with rows reading "nct6775 fan1 —", burying
  // the readings it exists to show under ones it cannot render.
  it("shows only temperature sensors, not the fans and rails beside them", () => {
    renderSensors(
      response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [
          {
            key: { chip: "nct6775", label: "CPU", kind: "temperature" },
            points: [[1_754_784_000_000, 45, 45]],
          },
          {
            // temp is null: a fan has no temperature.
            key: { chip: "nct6775", label: "CPU Fan", kind: "fan" },
            points: [[1_754_784_000_000, null, 1200]],
          },
          {
            key: { chip: "nct6775", label: "+12V", kind: "voltage" },
            points: [[1_754_784_000_000, null, 12.1]],
          },
        ],
      }),
    );

    const temperature = screen.getByRole("region", { name: /temperature/i });
    expect(within(temperature).getByText("CPU")).toBeInTheDocument();
    expect(within(temperature).getByText("45 °C")).toBeInTheDocument();
    // The non-temperature series must not appear in THIS panel -- an empty
    // tile is worse than an absent one, because it reads as a broken sensor.
    // Queried by tile name: all three kinds share the chip nct6775 here, so
    // the heading is in every card and only the tile tells them apart.
    expect(within(temperature).queryByText("CPU Fan")).toBeNull();
    expect(within(temperature).queryByText("+12V")).toBeNull();

    // They are not discarded, though: each kind gets its own card, in its
    // own unit, on its own scale. Putting a 1200 RPM fan on the same axis as
    // a 45 °C package is the reason they are separated rather than merged.
    const fans = screen.getByRole("region", { name: "Fans" });
    expect(within(fans).getByText("CPU Fan")).toBeInTheDocument();
    expect(within(fans).getByText("1200 RPM")).toBeInTheDocument();

    const power = screen.getByRole("region", { name: "Power" });
    expect(within(power).getByText("+12V")).toBeInTheDocument();
    expect(within(power).getByText("12.10 V")).toBeInTheDocument();
  });

  // A host with no fans -- every VM, every cloud instance -- must not carry
  // an empty "Fans" card. A card that is blank on most of the fleet teaches
  // people to stop reading this column.
  it("omits the fan and power cards on a host that reports neither", () => {
    renderSensors(
      response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [
          {
            key: {
              chip: "coretemp",
              label: "Package id 0",
              kind: "temperature",
            },
            points: [[1_754_784_000_000, 45, 45]],
          },
        ],
      }),
    );

    expect(
      screen.getByRole("region", { name: /temperature/i }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Fans" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Power" })).toBeNull();
  });

  // The same rule for the third sensor card. A VPS has no hwmon at all, and
  // the Temperature card was the one that stayed -- a heading over the words
  // "No temperature readings in this window" on every cloud instance in the
  // fleet.
  it("omits the temperature card on a host that reports no temperatures", () => {
    renderSensors(
      response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [],
      }),
    );

    expect(screen.queryByRole("region", { name: /temperature/i })).toBeNull();
    expect(screen.queryByRole("region", { name: "Fans" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Power" })).toBeNull();
  });

  // Seventeen coretemp readings in a 292px column was nine hundred pixels of
  // card, sixteen lines of it reading "coretemp Core N". The chip moves to a
  // heading, which is what pays for the tile, and the spread answers the
  // question the wall of numbers raises -- without any of the seventeen
  // leaving the page.
  it("groups a chip's many temperatures under one heading", () => {
    const cores = Array.from({ length: 17 }, (_, i) => ({
      key: {
        chip: "coretemp",
        label: i === 0 ? "Package id 0" : `Core ${i - 1}`,
        kind: "temperature",
      },
      points: [[1_754_784_000_000, 40 + i, 40 + i]],
    }));
    const { container } = renderSensors(
      response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: cores,
      }),
    );

    const temperature = screen.getByRole("region", { name: /temperature/i });
    expect(within(temperature).getByText("coretemp")).toBeInTheDocument();
    expect(
      within(temperature).getByText("17 sensors · 40–56 °C"),
    ).toBeInTheDocument();
    // Every one of the seventeen is still on the page, still its own
    // enlargeable chart -- the card got shorter, not shorter of readings.
    expect(container.querySelectorAll(".sensor-tile")).toHaveLength(17);
    expect(container.querySelectorAll(".sensor-tile svg")).toHaveLength(17);
  });

  // The tiles are one shape for every kind: SensorList draws all three cards,
  // and a board with ten fans must tile them the way a chip with ten
  // temperatures does.
  it("tiles a chip's many fans the same way", () => {
    const { container } = renderSensors(
      response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: Array.from({ length: 10 }, (_, i) => ({
          key: { chip: "nct6775", label: `fan${i}`, kind: "fan" },
          points: [[1_754_784_000_000, null, 1000 + i]],
        })),
      }),
    );

    expect(container.querySelectorAll(".sensor-tile")).toHaveLength(10);
    expect(container.querySelectorAll(".sensor-tile svg")).toHaveLength(10);
    expect(screen.getByText("10 sensors · 1000–1009 RPM")).toBeInTheDocument();
  });

  // drivetemp names every chip it registers "drivetemp" and publishes no
  // tempN_label, so the block device is the whole identity. Joining the label
  // in would print four tiles reading "temp1 sda", "temp1 sdb" -- the disk
  // behind a word that is the same on all four.
  it("names a drivetemp tile by its disk, not by its bare temp1 label", () => {
    renderSensors(
      response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind", "instance"],
        columns: ["temp", "value"],
        series: ["sda", "sdb", "sdc", "sdd"].map((disk, i) => ({
          key: {
            chip: "drivetemp",
            label: "temp1",
            kind: "temperature",
            instance: disk,
          },
          points: [[1_754_784_000_000, 33 + i, 33 + i]],
        })),
      }),
    );

    const temperature = screen.getByRole("region", { name: /temperature/i });
    for (const disk of ["sda", "sdb", "sdc", "sdd"]) {
      expect(within(temperature).getByText(disk)).toBeInTheDocument();
    }
    expect(within(temperature).queryByText("temp1 sda")).toBeNull();
    // A chip that publishes a real label beside an instance keeps both, and
    // the enlarge button keeps the full chip-and-label name either way.
    expect(
      within(temperature).getByRole("button", {
        name: "Enlarge temperature for drivetemp temp1 sdc",
      }),
    ).toBeInTheDocument();
  });

  // A fan's failure is its minimum. Averaged across a five-minute bucket a
  // stall is invisible -- the mean of a stopped fan and a spin-up is a
  // perfectly healthy number -- so the fan row must read value_min at the
  // rolled tiers, and must not fall back to the _avg that candidates()
  // prefers.
  it("reads a fan from value_min, not the average that hides a stall", () => {
    renderSensors(
      response({
        family: "sensor",
        tier: "5m",
        step_s: 300,
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:10:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:10:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["value_avg", "value_max", "value_min"],
        series: [
          {
            key: { chip: "nct6775", label: "fan2", kind: "fan" },
            // A bucket the fan spent partly stopped: the average and the
            // maximum both look fine, and only the minimum says so.
            points: [[1_754_784_300_000, 1180, 1400, 0]],
          },
        ],
      }),
    );

    const fans = screen.getByRole("region", { name: "Fans" });
    expect(within(fans).getByText("0 RPM")).toBeInTheDocument();
    expect(within(fans).queryByText("1180 RPM")).toBeNull();
  });
});
