import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, act, waitFor } from "@testing-library/react";
import { usePoll } from "./poll";
import { ApiError } from "./api";

function Probe({ fn, ms = 1000 }: { fn: () => Promise<string>; ms?: number }) {
  const { data, error, loading } = usePoll(fn, ms);
  return (
    <div>
      <span data-testid="data">{data ?? "none"}</span>
      <span data-testid="error">{error?.message ?? "none"}</span>
      <span data-testid="loading">{String(loading)}</span>
    </div>
  );
}

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => false,
  });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("usePoll", () => {
  it("fetches once immediately and then on the interval", async () => {
    const fn = vi.fn().mockResolvedValue("ok");
    render(<Probe fn={fn} />);

    await waitFor(() => expect(fn).toHaveBeenCalledTimes(1));
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(fn).toHaveBeenCalledTimes(3);
  });

  // A fleet overview left open in a background tab for a weekend is a
  // request a minute for two days, per tab, against a hub that is also
  // ingesting from every agent it owns -- invisible to whoever left the tab
  // open and indistinguishable, at the hub, from load that matters.
  it("does not fire while the tab is hidden", async () => {
    const fn = vi.fn().mockResolvedValue("ok");
    render(<Probe fn={fn} />);
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(1));

    act(() => setHidden(true));
    await act(async () => {
      vi.advanceTimersByTime(10_000);
    });

    expect(fn).toHaveBeenCalledTimes(1);
  });

  // A tab revealed after an hour must not sit on stale numbers until the
  // next tick.
  it("refreshes immediately when the tab comes back", async () => {
    const fn = vi.fn().mockResolvedValue("ok");
    render(<Probe fn={fn} />);
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(1));
    act(() => setHidden(true));

    await act(async () => setHidden(false));

    await waitFor(() => expect(fn).toHaveBeenCalledTimes(2));
  });

  // A chart that empties itself because one poll timed out says the host
  // stopped reporting, which is a different and far more alarming fact than
  // "the last refresh did not land".
  it("keeps the last good data when a poll fails", async () => {
    const fn = vi
      .fn()
      .mockResolvedValueOnce("first")
      .mockRejectedValue(new Error("network down"));
    render(<Probe fn={fn} />);
    await waitFor(() =>
      expect(screen.getByTestId("data")).toHaveTextContent("first"),
    );

    await act(async () => {
      vi.advanceTimersByTime(1000);
    });

    await waitFor(() =>
      expect(screen.getByTestId("error")).toHaveTextContent("network down"),
    );
    expect(screen.getByTestId("data")).toHaveTextContent("first");
  });

  // A 401 is a routing decision, not a message to render: the session
  // expired and the app must go to the login page. It reaches the caller as
  // an ApiError carrying its status, so the caller routes on the TYPE rather
  // than by parsing a message -- and it is not thrown, because a throw
  // inside a timer callback is an unhandled rejection nobody can catch.
  it("surfaces a 401 as an ApiError the caller can route on", async () => {
    const seen: (Error | null)[] = [];
    function Probe401() {
      const { error } = usePoll(
        () => Promise.reject(new ApiError(401, "unauthorized")),
        1000,
      );
      seen.push(error);
      return (
        <span data-testid="status">{String(error instanceof ApiError)}</span>
      );
    }
    render(<Probe401 />);

    await waitFor(() =>
      expect(screen.getByTestId("status")).toHaveTextContent("true"),
    );
    const last = seen.at(-1);
    expect(last).toBeInstanceOf(ApiError);
    expect((last as ApiError).status).toBe(401);
  });

  it("stops polling once unmounted", async () => {
    const fn = vi.fn().mockResolvedValue("ok");
    const { unmount } = render(<Probe fn={fn} />);
    await waitFor(() => expect(fn).toHaveBeenCalledTimes(1));

    unmount();
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });

    expect(fn).toHaveBeenCalledTimes(1);
  });
});
