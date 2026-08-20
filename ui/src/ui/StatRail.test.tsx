import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StatFigure, StatRail } from "./StatRail";

describe("StatFigure", () => {
  it("renders the value and the phrase that finishes it", () => {
    render(<StatFigure value={84} label="containers" />);
    expect(screen.getByText("84")).toBeInTheDocument();
    expect(screen.getByText("containers")).toBeInTheDocument();
  });

  it("renders an already-formatted figure verbatim", () => {
    render(<StatFigure value="155 kB/s" label="in + out" />);
    expect(screen.getByText("155 kB/s")).toBeInTheDocument();
  });

  // A rate is not a set, so there is no list of it to go to. A figure that
  // looks clickable and does nothing is worse than one that plainly is not.
  it("is inert without an href", () => {
    render(<StatFigure value="155 kB/s" label="in + out" />);
    expect(screen.queryByRole("link")).toBeNull();
  });

  /**
   * A real anchor, not a div with a handler. The figure sits directly above
   * the tab that shows what it counts, so it IS that control -- and
   * middle-click, cmd-click and copy-link have to keep working, which is the
   * whole reason it carries an href. Same rule Tabs.tsx follows.
   */
  it("is a real link when it counts something the page can show", () => {
    render(
      <StatFigure value={84} label="containers" href="/?entity=containers" />,
    );
    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "/?entity=containers",
    );
  });

  it("intercepts a plain click so the app navigates without a reload", async () => {
    const onSelect = vi.fn();
    render(
      <StatFigure
        value={84}
        label="containers"
        href="/?entity=containers"
        onSelect={onSelect}
      />,
    );

    await userEvent.click(screen.getByRole("link"));

    expect(onSelect).toHaveBeenCalled();
  });

  // The browser's own gestures stay the browser's: intercepting cmd-click
  // would take away the thing the href was used for.
  it("leaves a modified click to the browser", () => {
    const onSelect = vi.fn();
    render(
      <StatFigure
        value={84}
        label="containers"
        href="/?entity=containers"
        onSelect={onSelect}
      />,
    );

    // fireEvent, not userEvent: the claim is about the modifier flag on the
    // click event itself, which is what the handler reads.
    fireEvent.click(screen.getByRole("link"), { metaKey: true });

    expect(onSelect).not.toHaveBeenCalled();
  });

  it("uses the rail classes from index.css", () => {
    const { container } = render(
      <StatRail>
        <StatFigure value={84} label="containers" />
      </StatRail>,
    );
    expect(container.querySelector(".srail .s b")).toHaveTextContent("84");
    expect(container.querySelector(".srail .s .l")).toHaveTextContent(
      "containers",
    );
  });
});
