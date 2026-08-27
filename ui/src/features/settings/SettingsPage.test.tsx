import { describe, expect, it, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {
  SettingsPage,
  loadRange,
  loadView,
  RANGE_KEY,
  VIEW_KEY,
} from "./SettingsPage";

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
  it("shows the stored preferences as the pressed options", () => {
    localStorage.setItem(VIEW_KEY, "cards");
    localStorage.setItem(RANGE_KEY, "7d");
    render(<SettingsPage />);

    expect(
      pressed(screen.getByRole("group", { name: "Default fleet view" })),
    ).toEqual(["Cards"]);
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

  it("persists the default view and range for the next visit", async () => {
    const user = userEvent.setup();
    render(<SettingsPage />);

    await user.click(screen.getByRole("button", { name: "Cards" }));
    await user.click(screen.getByRole("button", { name: "7 d" }));

    expect(loadView()).toBe("cards");
    expect(loadRange()).toBe("7d");
  });

  it("falls back to the defaults when nothing, or nonsense, is stored", () => {
    expect(loadView()).toBe("table");
    expect(loadRange()).toBe("24h");

    localStorage.setItem(VIEW_KEY, "grid");
    localStorage.setItem(RANGE_KEY, "99y");
    expect(loadView()).toBe("table");
    expect(loadRange()).toBe("24h");
  });
});
