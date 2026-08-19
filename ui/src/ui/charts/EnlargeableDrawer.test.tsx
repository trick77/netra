import { describe, expect, it } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Enlargeable } from "./Enlargeable";

/**
 * A chart that has a page of its own.
 *
 * #108 made every one of these NAVIGATE on click, and it was right that a
 * chart needed somewhere to be: until then the deepest view in the app was the
 * only one with no URL. It was wrong about the common case. On the Graphs tab
 * a plain click threw away a grid of fifteen panels to look at one of them,
 * and coming back re-rendered it from the top -- so the gesture a reader makes
 * most often cost them the thing they were reading.
 *
 * The anchor stays, so the page is still reachable, still copyable, still
 * openable in a new tab. The plain left click opens a drawer beside the list
 * instead. These pin that split, because nothing else does: the tests that
 * came with #108 assert the href exists, which is still true either way.
 */
describe("Enlargeable with a page of its own", () => {
  const series = [{ name: "busy", color: "var(--s1)", values: [10, 12, 11] }];

  function renderLinked() {
    return render(
      <Enlargeable title="CPU" series={series} href="/hosts/7/chart/host-cpu">
        <svg data-testid="spark" />
      </Enlargeable>,
    );
  }

  it("is an anchor to the chart's page, so a new tab still works", () => {
    renderLinked();

    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "/hosts/7/chart/host-cpu",
    );
  });

  it("opens the drawer on a plain click instead of navigating", async () => {
    renderLinked();

    await userEvent.click(screen.getByRole("link"));

    const panel = screen.getByRole("dialog");
    expect(panel).toHaveClass("cd-drawer");
    // No backdrop. The list behind it has to stay readable AND clickable,
    // which is the whole reason this is a drawer rather than the modal -- a
    // scrim would make the surface inert and hand back the dialog's problem.
    expect(document.querySelector(".cd-back")).toBeNull();
    // And not modal, or a screen reader is told the rest of the page went
    // away at the moment its staying is the point.
    expect(panel).not.toHaveAttribute("aria-modal");
  });

  // A chart with nowhere else to be keeps the modal: there is no list to hold
  // open beside it, and a panel pinned to one edge of the window would be a
  // dialog that had given up half its width for nothing.
  it("keeps the modal dialog for a chart with no page", async () => {
    render(
      <Enlargeable title="CPU" series={series}>
        <svg />
      </Enlargeable>,
    );

    await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));

    const panel = screen.getByRole("dialog");
    expect(panel).not.toHaveClass("cd-drawer");
    expect(panel).toHaveAttribute("aria-modal", "true");
    expect(document.querySelector(".cd-back")).toBeInTheDocument();
  });

  // The browser's own gestures are left alone -- the same rule Tabs.tsx and
  // StatTile.tsx follow. Without this, "open in a new tab" would open a drawer
  // in the tab you were already in.
  // fireEvent rather than userEvent: the modifier has to be ON the click
  // event, which is what the guard reads, and userEvent's keyboard state only
  // carries through a `setup()` session. This asserts the guard directly.
  it.each([
    ["meta", { metaKey: true }],
    ["ctrl", { ctrlKey: true }],
    ["shift", { shiftKey: true }],
    ["middle button", { button: 1 }],
  ])("leaves a %s click to the browser", (_name, init) => {
    renderLinked();

    fireEvent.click(screen.getByRole("link"), init);

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // The drawer lies over the content; it does not push it aside. An earlier
  // version marked the root with data-drawer="on" and index.css put a 720px
  // padding-right on the content track, so opening one chart reflowed the
  // grid and moved every panel a reader was looking at. This guards the
  // absence of that: the document must be untouched while the drawer is open.
  it("leaves the document unmarked, so nothing behind it reflows", async () => {
    renderLinked();
    expect(document.documentElement.dataset.drawer).toBeUndefined();

    await userEvent.click(screen.getByRole("link"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(document.documentElement.dataset.drawer).toBeUndefined();

    await userEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }),
    );
    expect(document.documentElement.dataset.drawer).toBeUndefined();
  });

  // The fleet's four host charts pass no fetchSeries and no window, so their
  // drawer carries no range picker and no time axis -- the page does, and
  // before this a plain click went there. A reader must not have to know that
  // cmd-click exists to get back what the plain click used to give them.
  it("offers a visible way onward to the full page", async () => {
    renderLinked();

    await userEvent.click(screen.getByRole("link"));

    const onward = within(screen.getByRole("dialog")).getByRole("link", {
      name: "Open as page",
    });
    expect(onward).toHaveAttribute("href", "/hosts/7/chart/host-cpu");
  });

  // Nothing to link to: a chart with no page of its own is exactly the case
  // the modal still serves.
  it("offers no such link when the chart has no page", async () => {
    render(
      <Enlargeable title="CPU" series={series}>
        <svg />
      </Enlargeable>,
    );

    await userEvent.click(screen.getByRole("button", { name: /Enlarge/ }));

    expect(screen.queryByRole("link", { name: "Open as page" })).toBeNull();
  });

  it("closes on a click outside it", async () => {
    renderLinked();
    await userEvent.click(screen.getByRole("link"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    await userEvent.click(document.body);

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // Range buttons, the stats table and text selection all live in here, and
  // every one of them is a click that must not dismiss what it is inside.
  it("stays open when the click lands inside it", async () => {
    renderLinked();
    await userEvent.click(screen.getByRole("link"));

    await userEvent.click(screen.getByRole("dialog"));

    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("closes on Escape, like the dialog it replaces", async () => {
    renderLinked();
    await userEvent.click(screen.getByRole("link"));

    await userEvent.keyboard("{Escape}");

    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
