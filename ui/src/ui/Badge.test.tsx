import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Badge } from "./Badge";

describe("Badge", () => {
  // The rule this component exists to enforce: severity never rides on
  // colour alone. A tint with no word is indistinguishable from another
  // severity under deuteranopia (netra's accent vs. its critical red
  // measure ΔE 2.2 there) -- the label is what actually carries meaning.
  it("never renders severity without a text label", () => {
    render(<Badge severity="critical">silent</Badge>);
    expect(screen.getByText("silent")).toBeInTheDocument();
  });

  // A status badge is a dot and a word. The tinted chip that replaced the dot
  // for three commits made a page of fifty warned hosts fifty filled objects,
  // and the ground did the ranking the word is supposed to do.
  it("renders a dot and the label, and hides the dot from the reading", () => {
    const { container } = render(<Badge severity="ok">healthy</Badge>);
    const badge = container.querySelector(".badge")!;
    expect(badge).toHaveTextContent("healthy");
    // The dot carries the hue, the label carries the meaning (spec §3.3), so
    // the dot is decoration to a screen reader and the word is not.
    const dot = badge.querySelector(".dot")!;
    expect(dot).toBeInTheDocument();
    expect(dot).toHaveAttribute("aria-hidden", "true");
    // Nothing else: a status badge is exactly a dot and its word.
    expect(badge.children).toHaveLength(1);
  });

  it("applies the status class matching the severity", () => {
    const { container, rerender } = render(
      <Badge severity="warning">degraded</Badge>,
    );
    expect(container.querySelector(".badge")).toHaveClass("st-warn");

    rerender(<Badge severity="serious">at risk</Badge>);
    expect(container.querySelector(".badge")).toHaveClass("st-serious");

    rerender(<Badge severity="critical">down</Badge>);
    expect(container.querySelector(".badge")).toHaveClass("st-crit");

    rerender(<Badge severity="ok">up</Badge>);
    expect(container.querySelector(".badge")).toHaveClass("st-ok");
  });

  it("defaults to a neutral badge with no severity class when none is given", () => {
    const { container } = render(<Badge>plain</Badge>);
    const badge = container.querySelector(".badge");
    expect(badge).not.toHaveClass("st-ok");
    expect(badge).not.toHaveClass("st-warn");
    expect(badge).not.toHaveClass("st-serious");
    expect(badge).not.toHaveClass("st-crit");
    expect(screen.getByText("plain")).toBeInTheDocument();
  });

  // Not a runtime assertion -- this is documentation that the type system
  // is what enforces the rule. If `children` were optional, this file
  // would still compile with a severity-only, label-less badge.
  it("makes children a required prop at the type level", () => {
    // @ts-expect-error -- Badge must not be constructible without a label.
    const missingChildren = <Badge severity="critical" />;
    expect(missingChildren).toBeDefined();
  });
});
