import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Button } from "./Button";

describe("Button", () => {
  it("renders the base btn class plus the variant class", () => {
    render(<Button variant="primary">Save</Button>);
    const btn = screen.getByRole("button", { name: "Save" });
    expect(btn.className).toContain("btn");
    expect(btn.className).toContain("primary");
  });

  it("applies no extra variant class for the default secondary look", () => {
    render(<Button>Cancel</Button>);
    const btn = screen.getByRole("button", { name: "Cancel" });
    expect(btn.className.trim()).toBe("btn");
  });

  it("is genuinely disabled while busy and shows a spinner", () => {
    render(<Button busy>Saving</Button>);
    const btn = screen.getByRole("button", { name: "Saving" });
    expect(btn).toBeDisabled();
    expect(btn.querySelector("svg")).toBeTruthy();
  });

  it("does not fire onClick while busy", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <Button busy onClick={onClick}>
        Saving
      </Button>,
    );
    await user.click(screen.getByRole("button", { name: "Saving" }));
    expect(onClick).not.toHaveBeenCalled();
  });

  it("is reachable and operable by keyboard", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(<Button onClick={onClick}>Go</Button>);
    await user.tab();
    expect(screen.getByRole("button", { name: "Go" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});
