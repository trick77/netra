import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Segmented } from "./Segmented";

const options = [
  { value: "1h", label: "1h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
];

describe("Segmented", () => {
  it("marks exactly one option aria-pressed=true, matching value", () => {
    render(<Segmented options={options} value="24h" onChange={() => {}} />);
    const pressed = screen
      .getAllByRole("button")
      .filter((b) => b.getAttribute("aria-pressed") === "true");
    expect(pressed).toHaveLength(1);
    expect(pressed[0]).toHaveTextContent("24h");
  });

  it("calls onChange with the clicked option's value", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Segmented options={options} value="1h" onChange={onChange} />);
    await user.click(screen.getByRole("button", { name: "7d" }));
    expect(onChange).toHaveBeenCalledWith("7d");
  });

  it("is operable by keyboard", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Segmented options={options} value="1h" onChange={onChange} />);
    await user.tab();
    expect(screen.getByRole("button", { name: "1h" })).toHaveFocus();
    await user.tab();
    expect(screen.getByRole("button", { name: "24h" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onChange).toHaveBeenCalledWith("24h");
  });
});
