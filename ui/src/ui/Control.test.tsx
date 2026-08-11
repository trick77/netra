import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Input, Select } from "./Control";

describe("Control", () => {
  it("Input carries the ctl class and forwards native props", () => {
    render(<Input placeholder="Search" aria-label="Search" />);
    const input = screen.getByLabelText("Search");
    expect(input.className).toContain("ctl");
    expect(input).toHaveAttribute("placeholder", "Search");
  });

  it("Select carries the ctl class and renders its options", () => {
    render(
      <Select aria-label="Region">
        <option value="eu">EU</option>
        <option value="us">US</option>
      </Select>,
    );
    const select = screen.getByLabelText("Region");
    expect(select.className).toContain("ctl");
    expect(screen.getByRole("option", { name: "US" })).toBeInTheDocument();
  });

  it("Input forwards a ref to the native element", () => {
    let ref: HTMLInputElement | null = null;
    render(
      <Input
        aria-label="Ref target"
        ref={(el) => {
          ref = el;
        }}
      />,
    );
    expect(ref).toBeInstanceOf(HTMLInputElement);
  });
});
