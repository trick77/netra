import { describe, expect, it, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SettingsPage, loadRange, RANGE_KEY } from "./SettingsPage";

// test-setup.ts installs ONE MemoryStorage for the whole run and nothing
// clears it between tests, so a stored preference would leak into the next
// test's default.
beforeEach(() => {
  localStorage.clear();
});

function pressed(group: HTMLElement): string[] {
  return Array.from(group.querySelectorAll('button[aria-pressed="true"]')).map(
    (b) => b.textContent ?? "",
  );
}

describe("SettingsPage", () => {
  it("shows the stored preference as the pressed option", () => {
    localStorage.setItem(RANGE_KEY, "7d");
    render(<SettingsPage />);

    expect(
      pressed(screen.getByRole("group", { name: "Default time range" })),
    ).toEqual(["7 d"]);
  });

  // netra has one theme, so Settings offers no theme control and nothing
  // stamps the root. This asserts the absence rather than leaving it
  // untested: a Segmented reintroduced here would otherwise ship unnoticed.
  it("offers no theme control", () => {
    render(<SettingsPage />);

    expect(screen.queryByRole("group", { name: "Theme" })).toBeNull();
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
  });

  // The fleet has one rendering and one window, so there is nothing here to
  // default. Asserted rather than left untested: a control reintroduced for
  // a page that cannot honour it would otherwise ship unnoticed.
  it("offers no fleet view setting", () => {
    render(<SettingsPage />);

    expect(
      screen.queryByRole("group", { name: "Default fleet view" }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: "Cards" })).toBeNull();
  });

  it("persists the default range for the next visit", async () => {
    const user = userEvent.setup();
    render(<SettingsPage />);

    await user.click(screen.getByRole("button", { name: "7 d" }));

    expect(loadRange()).toBe("7d");
  });

  // The fleet's own window is offered here too. It is not the fleet that
  // reads this -- that page has one window and no picker -- but an enlarged
  // chart opens on it, and a reader who wants 12h everywhere should be able
  // to say so once.
  it("offers the fleet's window as a default too", () => {
    render(<SettingsPage />);

    expect(screen.getByRole("button", { name: "12 h" })).toBeInTheDocument();
  });

  it("falls back to the default when nothing, or nonsense, is stored", () => {
    expect(loadRange()).toBe("24h");

    localStorage.setItem(RANGE_KEY, "99y");
    expect(loadRange()).toBe("24h");
  });
});
