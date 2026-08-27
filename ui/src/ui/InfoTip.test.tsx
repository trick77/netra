import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InfoTip } from "./InfoTip";

const TEXT = "Connections the kernel dropped before the process accepted them.";

describe("InfoTip", () => {
  it("says what it is about, so a grid of them stays tellable apart", () => {
    render(<InfoTip text={TEXT} label="TCP listen queue" />);

    expect(
      screen.getByRole("button", { name: "About TCP listen queue" }),
    ).toBeTruthy();
  });

  it("keeps the text out of the document until it is asked for", () => {
    render(<InfoTip text={TEXT} label="TCP listen queue" />);

    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("opens on hover", async () => {
    const user = userEvent.setup();
    render(<InfoTip text={TEXT} label="TCP listen queue" />);

    await user.hover(screen.getByRole("button"));

    expect(screen.getByRole("tooltip").textContent).toBe(TEXT);
  });

  it("closes when the pointer leaves", async () => {
    const user = userEvent.setup();
    render(<InfoTip text={TEXT} label="TCP listen queue" />);

    await user.hover(screen.getByRole("button"));
    await user.unhover(screen.getByRole("button"));

    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  // A reader who never touches the mouse gets the same affordance: the glyph
  // is in the tab order and focusing it is the hover.
  it("opens on keyboard focus and describes the button while open", async () => {
    const user = userEvent.setup();
    render(<InfoTip text={TEXT} label="TCP listen queue" />);

    await user.tab();

    const button = screen.getByRole("button");
    expect(button).toBe(document.activeElement);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-describedby")).toBe(
      screen.getByRole("tooltip").id,
    );
  });

  // The touch path: a finger fires pointerenter and then click, and never
  // fires pointerleave at all. Without the click the bubble would be
  // unreachable; without Escape or a press elsewhere it would never close.
  it("closes on Escape", async () => {
    const user = userEvent.setup();
    render(<InfoTip text={TEXT} label="TCP listen queue" />);

    await user.click(screen.getByRole("button"));
    expect(screen.getByRole("tooltip")).toBeTruthy();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("closes when something else is pressed", async () => {
    const user = userEvent.setup();
    render(
      <div>
        <InfoTip text={TEXT} label="TCP listen queue" />
        <button type="button">elsewhere</button>
      </div>,
    );

    await user.click(screen.getByRole("button", { name: /About/ }));
    await user.click(screen.getByRole("button", { name: "elsewhere" }));

    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
