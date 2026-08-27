import { describe, expect, it } from "vitest";
import { ALL_SPECS } from "./chartSpecs";

/**
 * An editorial guard, not a rendering one.
 *
 * `about` exists because a third of these panels are titled in the kernel's
 * words, and it stops being read the moment it turns into documentation. Two
 * failure modes are worth a test: a stub that says nothing, and a paragraph
 * nobody finishes. Both are cheap to introduce one spec at a time.
 */
describe("panel explanations", () => {
  const explained = ALL_SPECS.filter((spec) => spec.about !== undefined);

  it("are given to the panels whose titles are not enough", () => {
    // Not a count anyone should chase -- it is here so that deleting the lot,
    // or bolting one onto every spec, fails rather than passes quietly.
    expect(explained.length).toBeGreaterThan(20);
    expect(explained.length).toBeLessThan(ALL_SPECS.length);
  });

  it("say something, and stop", () => {
    for (const spec of explained) {
      const about = spec.about as string;
      expect(about.trim(), spec.slug).toBe(about);
      expect(about.length, spec.slug).toBeGreaterThan(40);
      // Three sentences is the brief. A fourth is a manual page.
      const sentences = about.split(". ").length;
      expect(sentences, spec.slug).toBeLessThanOrEqual(3);
      expect(about.endsWith("."), spec.slug).toBe(true);
    }
  });

  // The title is already on screen an inch away; repeating it is the first
  // sentence of every explanation nobody reads.
  it("do not open by restating the title", () => {
    for (const spec of explained) {
      const about = spec.about as string;
      expect(
        about.toLowerCase().startsWith(spec.title.toLowerCase()),
        spec.slug,
      ).toBe(false);
    }
  });
});
