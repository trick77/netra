import { describe, expect, it } from "vitest";
import { renderHook } from "@testing-library/react";
import { PRODUCT, SEP, tabTitle, useTabTitle } from "./title";

describe("tabTitle", () => {
  it("leads with the product and joins with an en-spaced middot", () => {
    expect(SEP).toBe(" · ");
    expect(tabTitle("web01", "Graphs")).toBe("Netra · web01 · Graphs");
  });

  it("drops parts that are still loading", () => {
    expect(tabTitle(undefined, "Graphs")).toBe("Netra · Graphs");
    expect(tabTitle(null, "")).toBe(PRODUCT);
  });
});

describe("useTabTitle", () => {
  it("sets the title and puts the product back on unmount", () => {
    const { rerender, unmount } = renderHook(
      ({ host }: { host?: string }) => useTabTitle(host, "Storage"),
      { initialProps: {} as { host?: string } },
    );
    expect(document.title).toBe("Netra · Storage");

    rerender({ host: "db1" });
    expect(document.title).toBe("Netra · db1 · Storage");

    unmount();
    expect(document.title).toBe(PRODUCT);
  });
});
