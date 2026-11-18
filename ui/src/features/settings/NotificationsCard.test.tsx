import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../lib/api", async () => ({
  ...(await vi.importActual<typeof import("../../lib/api")>("../../lib/api")),
  getConfig: vi.fn(),
  testNotify: vi.fn(),
}));

import * as api from "../../lib/api";
import { NotificationsCard } from "./NotificationsCard";

beforeEach(() => {
  vi.mocked(api.getConfig).mockReset();
  vi.mocked(api.testNotify).mockReset();
});

describe("NotificationsCard", () => {
  // The question an operator with no mail is asking: is anything configured
  // at all? Answered without sending, and with where to fix it.
  it("says notifications are off, and where to turn them on", async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      hub_url: "",
      notify_channels: [],
      notify_timeout_s: 45,
    });
    render(<NotificationsCard />);
    expect(
      await screen.findByText(/Off: no channel is configured/),
    ).toHaveTextContent("BACKEND_NOTIFY_URLS");
    expect(
      screen.queryByRole("button", { name: "Send test notification" }),
    ).toBeNull();
  });

  it("names the channels and reports each one's test result", async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      hub_url: "",
      notify_channels: ["channel 1 (smtp)", "channel 2 (ntfy)"],
      notify_timeout_s: 45,
    });
    vi.mocked(api.testNotify).mockResolvedValue([
      { channel: "channel 1 (smtp)", ok: true },
      {
        channel: "channel 2 (ntfy)",
        ok: false,
        error: "unexpected response status code: 502",
      },
    ]);
    render(<NotificationsCard />);
    expect(
      await screen.findByText(
        /Sending to channel 1 \(smtp\), channel 2 \(ntfy\)/,
      ),
    ).toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("button", { name: "Send test notification" }),
    );
    const results = await screen.findByRole("list", { name: "Test results" });
    const items = within(results).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent(
      "channel 1 (smtp): accepted, check that it arrived",
    );
    expect(items[1]).toHaveTextContent(
      "channel 2 (ntfy): failed — unexpected response status code: 502",
    );
    expect(items[1]).toHaveClass("failed");
  });

  it("says why a test could not run", async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      hub_url: "",
      notify_channels: ["channel 1 (smtp)"],
      notify_timeout_s: 45,
    });
    vi.mocked(api.testNotify).mockRejectedValue(
      new api.ApiError(
        409,
        "a test notification is still being sent; try again when it has finished",
      ),
    );
    render(<NotificationsCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Send test notification" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "a test notification is still being sent; try again when it has finished",
    );
  });

  it("keeps the button busy while the channels answer", async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      hub_url: "",
      notify_channels: ["channel 1 (smtp)"],
      notify_timeout_s: 45,
    });
    vi.mocked(api.testNotify).mockReturnValue(new Promise(() => {}));
    render(<NotificationsCard />);
    const button = await screen.findByRole("button", {
      name: "Send test notification",
    });
    await userEvent.click(button);
    expect(button).toBeDisabled();
    expect(screen.getByText(/up to 45 s…/)).toBeInTheDocument();
  });

  it("says when the hub's configuration could not be read", async () => {
    vi.mocked(api.getConfig).mockRejectedValue(new Error("internal error"));
    render(<NotificationsCard />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "configuration could not be read: internal error",
    );
  });

  // An expired session is the screen's to route to the login page; the card
  // hands every failure over.
  it("hands a failed call to the screen", async () => {
    const expired = new api.ApiError(401, "unauthorized");
    vi.mocked(api.getConfig).mockRejectedValue(expired);
    const onError = vi.fn();
    render(<NotificationsCard onError={onError} />);
    await screen.findByRole("alert");
    expect(onError).toHaveBeenCalledWith(expired);
  });
});
