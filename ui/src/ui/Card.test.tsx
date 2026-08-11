import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Card } from "./Card";

describe("Card", () => {
  it("renders the card shell with body content", () => {
    render(
      <Card>
        <p>hello</p>
      </Card>,
    );
    expect(screen.getByText("hello")).toBeInTheDocument();
  });

  it("renders a header with the title when given", () => {
    render(<Card title="Latency">content</Card>);
    expect(
      screen.getByRole("heading", { name: "Latency" }),
    ).toBeInTheDocument();
  });

  it("renders the action alongside the title", () => {
    render(
      <Card title="Latency" action={<button>Refresh</button>}>
        content
      </Card>,
    );
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
  });

  it("omits the header entirely when neither title nor action is given", () => {
    render(<Card>content</Card>);
    const card = screen.getByText("content").closest(".card")!;
    expect(card.querySelector("header")).toBeNull();
  });

  it("applies the card class to the outer element", () => {
    render(<Card>content</Card>);
    expect(screen.getByText("content").closest(".card")).not.toBeNull();
  });
});
