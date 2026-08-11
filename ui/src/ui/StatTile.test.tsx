import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { StatTile } from "./StatTile";

describe("StatTile", () => {
  it("renders the label and value", () => {
    render(<StatTile label="Hosts up" value={12} />);
    expect(screen.getByText("Hosts up")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();
  });

  it("renders an optional unit alongside the value", () => {
    render(<StatTile label="CPU" value={42} unit="%" />);
    expect(screen.getByText(/42/)).toBeInTheDocument();
    expect(screen.getByText(/%/)).toBeInTheDocument();
  });

  it("renders an optional detail line", () => {
    render(
      <StatTile label="Uptime" value="14 d" detail="since last restart" />,
    );
    expect(screen.getByText("since last restart")).toBeInTheDocument();
  });

  it("omits the detail line when none is given", () => {
    const { container } = render(<StatTile label="Hosts up" value={12} />);
    expect(container.querySelector(".d")).not.toBeInTheDocument();
  });

  // The one large number on a page uses proportional figures (design
  // system default); tabular-nums is reserved for columns of numbers,
  // which is a different component's job, not this one's.
  it("does not force tabular-nums on the headline value", () => {
    const { container } = render(<StatTile label="Hosts up" value={12} />);
    const valueEl = container.querySelector(".v") as HTMLElement;
    expect(valueEl.className.split(" ")).not.toContain("tabular-nums");
    expect(valueEl.style.fontVariantNumeric).not.toBe("tabular-nums");
  });

  it("uses the tile classes from index.css", () => {
    const { container } = render(<StatTile label="Hosts up" value={12} />);
    expect(container.querySelector(".tile")).toBeInTheDocument();
    expect(container.querySelector(".tile .k")).toHaveTextContent("Hosts up");
    expect(container.querySelector(".tile .v")).toHaveTextContent("12");
  });
});
