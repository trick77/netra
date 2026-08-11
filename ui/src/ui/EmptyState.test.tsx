import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Inbox } from "lucide-react";
import { EmptyState } from "./EmptyState";

describe("EmptyState", () => {
  it("renders title and body text", () => {
    render(
      <EmptyState
        icon={Inbox}
        title="No hosts yet"
        body="Add a host to start monitoring."
      />,
    );
    expect(screen.getByText("No hosts yet")).toBeInTheDocument();
    expect(
      screen.getByText("Add a host to start monitoring."),
    ).toBeInTheDocument();
  });

  it("renders the icon", () => {
    render(<EmptyState icon={Inbox} title="Empty" body="Nothing here." />);
    const svg = document.querySelector("svg");
    expect(svg).not.toBeNull();
  });

  it("renders the action and fires its click handler", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    render(
      <EmptyState
        icon={Inbox}
        title="Empty"
        body="Nothing here."
        action={<button onClick={onClick}>Add host</button>}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Add host" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("omits the action region when none is given", () => {
    render(<EmptyState icon={Inbox} title="Empty" body="Nothing here." />);
    expect(screen.queryByRole("button")).toBeNull();
  });
});
