import { describe, expect, it, beforeEach } from "vitest";
import { applyTheme, loadTheme } from "./theme";

describe("theme", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("stamps an explicit choice on the root element", () => {
    applyTheme("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(loadTheme()).toBe("dark");
  });

  it("removes the stamp for system, so the media query decides", () => {
    applyTheme("dark");
    applyTheme("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    expect(loadTheme()).toBe("system");
  });

  it("defaults to system when nothing is stored", () => {
    expect(loadTheme()).toBe("system");
  });
});
