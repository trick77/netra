import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StatTile } from "./StatTile";
import { ABSENT } from "../lib/format";

const values = [1, 2, null, 4];

describe("StatTile", () => {
  it("prints the figure and its unit as given", () => {
    render(<StatTile label="CPU" value="37" unit="%" values={values} />);

    expect(screen.getByText("37")).toBeInTheDocument();
    expect(screen.getByText("%")).toBeInTheDocument();
  });

  // The caller formats, and a null reading arrives as ABSENT. A tile that
  // rendered its own 0 for "not collected" is the exact confusion every
  // formatter in lib/format.ts exists to prevent.
  it("shows the caller's absent marker rather than inventing a zero", () => {
    render(<StatTile label="Swap in" value={ABSENT} values={[]} />);

    expect(screen.getByText(ABSENT)).toBeInTheDocument();
    expect(screen.queryByText("0")).toBeNull();
  });

  it("writes no sub-line at all when there is none, rather than a dash", () => {
    const { container } = render(
      <StatTile label="Interrupts" value="41" values={values} />,
    );

    expect(container.querySelector(".sub")).toBeNull();
  });

  it("draws its own trend, labelled for a screen reader", () => {
    render(<StatTile label="CPU" value="37" values={values} />);

    expect(screen.getByRole("img", { name: "CPU trend" })).toBeInTheDocument();
  });

  // Neutral is the default, and it is the whole colour rule: the fixed status
  // palette means severity on this page, so a tile whose reading crossed
  // nothing must not wear one.
  it("carries no status class without a severity", () => {
    const { container } = render(
      <StatTile label="Context switches" value="51" values={values} />,
    );

    expect(container.querySelector(".tile")?.className).toBe("tile");
  });

  it("names its severity in the class so the tint, rule and figure agree", () => {
    const { container } = render(
      <StatTile
        label="Busiest filesystem"
        value="94"
        unit="%"
        severity="warning"
        values={values}
      />,
    );

    expect(container.querySelector(".tile")?.className).toContain(
      "sev-warning",
    );
  });

  // A tile with a chart behind it is a link, not a div with a handler: the
  // same rule StatFigure follows, so cmd-click, middle-click and copy-link
  // keep working without this component reimplementing any of them.
  it("is a real link when it has a chart to open", () => {
    render(
      <StatTile
        label="CPU"
        value="37"
        values={values}
        href="/hosts/7/chart/host-cpu"
      />,
    );

    expect(screen.getByRole("link", { name: /CPU/ })).toHaveAttribute(
      "href",
      "/hosts/7/chart/host-cpu",
    );
  });

  it("is inert, not a link, when no panel draws its column", () => {
    render(<StatTile label="Forks" value="122" values={values} />);

    expect(screen.queryByRole("link")).toBeNull();
  });

  it("intercepts a plain click so the app navigates itself", async () => {
    const onSelect = vi.fn();
    render(
      <StatTile
        label="CPU"
        value="37"
        values={values}
        href="/hosts/7/chart/host-cpu"
        onSelect={onSelect}
      />,
    );

    await userEvent.click(screen.getByRole("link", { name: /CPU/ }));

    expect(onSelect).toHaveBeenCalledOnce();
  });

  // The half that makes the href worth having: a modified click belongs to
  // the browser, and intercepting it would take away the thing a real link
  // was used for.
  it("leaves a cmd-click to the browser", async () => {
    const onSelect = vi.fn();
    render(
      <StatTile
        label="CPU"
        value="37"
        values={values}
        href="/hosts/7/chart/host-cpu"
        onSelect={onSelect}
      />,
    );

    // fireEvent, not userEvent: the modifier has to be ON the click event,
    // and that is exactly what the handler reads.
    fireEvent.click(screen.getByRole("link", { name: /CPU/ }), {
      metaKey: true,
    });

    expect(onSelect).not.toHaveBeenCalled();
  });
});
