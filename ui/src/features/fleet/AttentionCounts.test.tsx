// The line that replaced the attention band. What it has to get right is
// bounded height (one entry per KIND, never per host) and real links, since
// the band's own overflow was a dead end: "+30 more hosts" with nothing to
// click.
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AttentionCounts, type CountTile } from "./AttentionCounts";

const UNITS: CountTile = {
  kind: "failed-units",
  severity: "warning",
  label: "Failed units",
  ids: Array.from({ length: 31 }, (_, i) => String(i + 1)),
};
const DISK: CountTile = {
  kind: "disk",
  severity: "critical",
  label: "Filesystem nearly full",
  ids: ["40", "41"],
};

describe("AttentionCounts", () => {
  it("states the kind and how many hosts have it, in one entry", () => {
    render(
      <AttentionCounts kinds={[UNITS]} active={null} onSelect={() => {}} />,
    );

    const list = screen.getByRole("list", { name: /by kind/i });
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(within(list).getByText("Failed units")).toBeInTheDocument();
    expect(within(list).getByText("31")).toBeInTheDocument();
    // The noun is gone from the chip: on a page of hosts, a count beside a
    // kind is a count of hosts, and "31 hosts" put the widest word in the
    // chip on the one thing the reader already knows. The severity word
    // stays, because hue is never the only channel that carries it.
    expect(within(list).queryByText(/hosts/)).toBeNull();
    expect(within(list).getByText("Warning")).toBeInTheDocument();
  });

  // The chip is a count and a kind, with a dot for the severity and the word
  // after it. One host reads "1", not "1 host": there is no noun left to
  // pluralise, which is one fewer thing that can be wrong on a fleet of one.
  it("counts one host as a bare figure", () => {
    const { container } = render(
      <AttentionCounts
        kinds={[{ ...UNITS, ids: ["1"] }]}
        active={null}
        onSelect={() => {}}
      />,
    );

    expect(screen.getByText("1")).toBeInTheDocument();
    expect(screen.queryByText(/hosts?$/)).toBeNull();
    expect(container.querySelector(".atile .dot")).toBeInTheDocument();
  });

  // The dot beside the kind is a mark, not a word, so the severity WORD is
  // what keeps meaning off colour alone (spec §3.3). The kind's own name cannot stand in for it: "Failed units"
  // says what is wrong and not how bad it is.
  it("names the severity rather than leaving it to the colour", () => {
    const { container } = render(
      <AttentionCounts
        kinds={[DISK, UNITS]}
        active={null}
        onSelect={() => {}}
      />,
    );

    expect(screen.getByText("Critical")).toBeInTheDocument();
    expect(screen.getByText("Warning")).toBeInTheDocument();
    expect(container.querySelector(".atile.st-crit")).toBeInTheDocument();
    expect(container.querySelector(".atile.st-warn")).toBeInTheDocument();
    expect(screen.getByText("Filesystem nearly full")).toBeInTheDocument();
  });

  // A link, so a filtered fleet is something you can send someone. App
  // delegates in-origin anchor clicks to the router, so the href is the
  // navigation and the handler is only for the uncontrolled case.
  it("carries the filter in a real href", () => {
    render(
      <AttentionCounts kinds={[UNITS]} active={null} onSelect={() => {}} />,
    );

    expect(screen.getByRole("link", { name: /Failed units/ })).toHaveAttribute(
      "href",
      "/?attn=failed-units",
    );
  });

  it("selects the kind that was clicked", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <AttentionCounts kinds={[UNITS]} active={null} onSelect={onSelect} />,
    );

    await user.click(screen.getByRole("link", { name: /Failed units/ }));
    expect(onSelect).toHaveBeenCalledWith("failed-units");
  });

  // Clicking the one you are already in is the way back out -- otherwise the
  // only exit is a "show all" link somewhere else on the page.
  it("clears the filter when the active kind is clicked again", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <AttentionCounts
        kinds={[UNITS]}
        active="failed-units"
        onSelect={onSelect}
      />,
    );

    const link = screen.getByRole("link", { name: /Failed units/ });
    expect(link).toHaveAttribute("aria-current", "true");
    expect(link).toHaveAttribute("href", "/");
    await user.click(link);
    expect(onSelect).toHaveBeenCalledWith("all");
  });

  // Modified clicks belong to the browser: an href is used precisely so
  // cmd-click still opens a tab.
  it("leaves a cmd-click to the browser", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <AttentionCounts kinds={[UNITS]} active={null} onSelect={onSelect} />,
    );

    await user.keyboard("{Meta>}");
    await user.click(screen.getByRole("link", { name: /Failed units/ }));
    await user.keyboard("{/Meta}");
    expect(onSelect).not.toHaveBeenCalled();
  });

  // Nothing wrong, nothing to say. The page's own all-clear line covers it.
  it("renders nothing when nothing is wrong", () => {
    const { container } = render(
      <AttentionCounts kinds={[]} active={null} onSelect={() => {}} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
