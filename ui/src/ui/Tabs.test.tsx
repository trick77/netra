import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Tabs } from "./Tabs";

const items = [
  { id: "graphs", label: "Graphs", href: "/hosts/1/graphs" },
  { id: "logs", label: "Logs", href: "/hosts/1/logs", badge: "3" },
  { id: "config", label: "Config", href: "/hosts/1/config" },
];

describe("Tabs", () => {
  it("renders one real anchor per item, with real hrefs", () => {
    render(<Tabs items={items} active="graphs" />);
    const links = screen.getAllByRole("link");
    expect(links).toHaveLength(3);
    expect(screen.getByRole("link", { name: "Graphs" })).toHaveAttribute(
      "href",
      "/hosts/1/graphs",
    );
    expect(screen.getByRole("link", { name: "Logs 3" })).toHaveAttribute(
      "href",
      "/hosts/1/logs",
    );
  });

  it("marks exactly one item as current", () => {
    render(<Tabs items={items} active="logs" />);
    const current = screen
      .getAllByRole("link")
      .filter((el) => el.hasAttribute("aria-current"));
    expect(current).toHaveLength(1);
    expect(current[0]).toHaveTextContent("Logs");
    expect(current[0]).toHaveAttribute("aria-current", "page");
  });

  it("renders a badge when the item has one", () => {
    render(<Tabs items={items} active="graphs" />);
    expect(screen.getByText("3")).toBeInTheDocument();
  });

  it("calls onChange with the item id when a tab is clicked", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    const onChange = (id: string) => calls.push(id);
    const calls: string[] = [];
    render(<Tabs items={items} active="graphs" onChange={onChange} />);
    await user.click(screen.getByRole("link", { name: "Config" }));
    expect(calls).toEqual(["config"]);
  });
});
