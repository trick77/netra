import { describe, expect, it, vi, afterEach } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { SinceLastCheck } from "./SinceLastCheck";

const NOW = new Date("2026-08-10T14:00:00Z");

afterEach(() => {
  vi.useRealTimers();
});

describe("SinceLastCheck", () => {
  it("states the age of the check it was given", () => {
    render(<SinceLastCheck checkedAt="2026-08-10T13:59:20Z" now={NOW} />);

    // The age alone, with the words that finish it as its label: the rail
    // states figures, and "40 s ago since last check" is not a sentence.
    expect(screen.getByText("40 s")).toBeInTheDocument();
    expect(screen.getByText("since last check")).toBeInTheDocument();
  });

  // The whole reason this is a component. Read off the page's own `now` the
  // figure was recomputed only when a poll landed -- the same event that
  // reset it -- so it said "0 s" at every paint and could never show a poll
  // that had stalled.
  it("counts up between polls instead of standing at the poll's own instant", () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);

    render(<SinceLastCheck checkedAt={NOW.toISOString()} />);
    expect(screen.getByText("0 s")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(screen.getByText("5 s")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(screen.getByText("1 m 5 s")).toBeInTheDocument();
  });

  // A landed poll has to snap the figure back at once rather than at the next
  // tick: a rail that reads "1 m 5 s" for a second after a successful refresh
  // is reporting a stall that is over.
  it("returns to zero the moment a newer check arrives", () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);

    const { rerender } = render(
      <SinceLastCheck checkedAt="2026-08-10T13:59:20Z" />,
    );
    expect(screen.getByText("40 s")).toBeInTheDocument();

    rerender(<SinceLastCheck checkedAt={NOW.toISOString()} />);
    expect(screen.getByText("0 s")).toBeInTheDocument();
  });

  // An injected `now` has to hold for the life of the component, not for its
  // first second: a tick that read the wall clock made every assertion past
  // one second race the real time of day, which is what the prop exists to
  // prevent.
  it("advances from an injected now rather than jumping to the wall clock", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2020-01-01T00:00:00Z"));

    render(<SinceLastCheck checkedAt="2026-08-10T13:59:20Z" now={NOW} />);
    expect(screen.getByText("40 s")).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(20_000);
    });
    expect(screen.getByText("1 m")).toBeInTheDocument();
  });

  // ABSENT under "since last check" reads as "the check failed", which is a
  // claim the page has no basis for. The AGE is what decides, not the
  // timestamp: an unparseable string is the same fact as none at all, and
  // guarding on the timestamp alone let a malformed one render the very em
  // dash this omits.
  it("renders nothing when there is no age to state", () => {
    const { container, rerender } = render(
      <SinceLastCheck checkedAt={null} now={NOW} />,
    );
    expect(container).toBeEmptyDOMElement();

    rerender(<SinceLastCheck checkedAt="not a timestamp" now={NOW} />);
    expect(container).toBeEmptyDOMElement();
  });

  // Nothing to count, nothing to run. Left unconditional the interval woke
  // the component once a second, forever, to render null.
  it("runs no clock when there is nothing to count", () => {
    vi.useFakeTimers();

    const { rerender } = render(<SinceLastCheck checkedAt={null} />);
    expect(vi.getTimerCount()).toBe(0);

    rerender(<SinceLastCheck checkedAt="not a timestamp" />);
    expect(vi.getTimerCount()).toBe(0);

    rerender(<SinceLastCheck checkedAt={NOW.toISOString()} />);
    expect(vi.getTimerCount()).toBe(1);
  });

  // A clock a moment ahead of this one must not produce a negative age.
  it("clamps a check stamped in the future to zero", () => {
    render(<SinceLastCheck checkedAt="2026-08-10T14:00:30Z" now={NOW} />);

    expect(screen.getByText("0 s")).toBeInTheDocument();
  });
});
