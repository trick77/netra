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
// test's default. The root attribute is global for the same reason.
beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});

function pressed(group: HTMLElement): string[] {
  return Array.from(group.querySelectorAll('button[aria-pressed="true"]')).map(
    (b) => b.textContent ?? "",
  );
}

describe("SettingsPage", () => {
  it("shows the stored preferences as the pressed options", () => {
    localStorage.setItem("netra.theme", "dark");
    localStorage.setItem(VIEW_KEY, "cards");
    localStorage.setItem(RANGE_KEY, "7d");
    render(<SettingsPage />);

    expect(pressed(screen.getByRole("group", { name: "Theme" }))).toEqual([
      "Dark",
    ]);
    expect(
      pressed(screen.getByRole("group", { name: "Default overview view" })),
    ).toEqual(["Cards"]);
    expect(
      pressed(screen.getByRole("group", { name: "Default time range" })),
    ).toEqual(["7 d"]);
  });

  it("stamps an explicit theme on the root element", async () => {
    const user = userEvent.setup();
    render(<SettingsPage />);

    await user.click(screen.getByRole("button", { name: "Dark" }));

    expect(document.documentElement.dataset.theme).toBe("dark");
  });

  it("removes data-theme for System, so the page follows the OS live", async () => {
    const user = userEvent.setup();
    render(<SettingsPage />);

    await user.click(screen.getByRole("button", { name: "Dark" }));
    await user.click(screen.getByRole("button", { name: "System" }));

    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(localStorage.getItem("netra.theme")).toBe("system");
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
