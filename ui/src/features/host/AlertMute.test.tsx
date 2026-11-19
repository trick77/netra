import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../lib/api", async () => ({
  ...(await vi.importActual<typeof import("../../lib/api")>("../../lib/api")),
  getMutes: vi.fn(),
  createMute: vi.fn(),
  deleteMute: vi.fn(),
}));

import * as api from "../../lib/api";
import type { Mute } from "../../lib/api";
import { AlertMute } from "./AlertMute";
import type { MuteTarget } from "./mutes";
import { useMutes } from "./useMutes";

const mute = (over: Partial<Mute>): Mute => ({
  id: 1,
  host_id: 7,
  hostname: "nas",
  kind: "silent",
  subject: "",
  until_resolved: false,
  until: null,
  note: "",
  created: "2026-11-18T10:00:00Z",
  ...over,
});

const silent: MuteTarget = { kind: "silent", subject: "", open: true };

function Line({ target = silent }: { target?: MuteTarget }) {
  return <AlertMute hostId={7} target={target} mutes={useMutes(7)} />;
}

beforeEach(() => {
  vi.mocked(api.getMutes).mockReset();
  vi.mocked(api.createMute).mockReset();
  vi.mocked(api.deleteMute).mockReset();
});

describe("AlertMute", () => {
  it("mutes the alert until resolved by default, and says so at once", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    vi.mocked(api.createMute).mockResolvedValue(
      mute({ id: 5, until_resolved: true }),
    );

    render(<Line />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute Stopped reporting" }),
    );
    expect(
      screen.getByRole("button", { name: "Until resolved" }),
    ).toHaveAttribute("aria-pressed", "true");
    // The re-read has not caught up yet; the line must not offer Mute again.
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));

    expect(api.createMute).toHaveBeenCalledWith({
      host_id: 7,
      kind: "silent",
      subject: "",
      until_resolved: true,
      minutes: 0,
    });
    expect(await screen.findByText("muted until resolved")).toBeInTheDocument();
  });

  it("mutes permanently, for an alert that keeps coming back", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    vi.mocked(api.createMute).mockResolvedValue(mute({}));

    render(<Line />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute Stopped reporting" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Permanently" }));
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));
    expect(api.createMute).toHaveBeenCalledWith({
      host_id: 7,
      kind: "silent",
      subject: "",
      until_resolved: false,
      minutes: 0,
    });
    expect(await screen.findByText("muted permanently")).toBeInTheDocument();
  });

  it("offers no until resolved when nothing is open, and says how far it reaches", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    vi.mocked(api.createMute).mockResolvedValue(
      mute({ kind: "failed-units", until: "2026-11-18T11:00:00Z" }),
    );

    render(
      <Line
        target={{
          kind: "failed-units",
          subject: "",
          open: false,
          reach: "Mutes every failed unit on this host.",
        }}
      />,
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute Failed units" }),
    );
    expect(
      screen.queryByRole("button", { name: "Until resolved" }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "1 h" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(
      screen.getByText("Mutes every failed unit on this host."),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "24 h" }));
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));
    expect(api.createMute).toHaveBeenCalledWith(
      expect.objectContaining({ until_resolved: false, minutes: 1440 }),
    );
  });

  it("says why a mute failed, and Cancel starts over", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    vi.mocked(api.createMute).mockRejectedValue(
      new Error("nothing of that kind is open"),
    );

    render(<Line />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute Stopped reporting" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "24 h" }));
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "nothing of that kind is open",
    );

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Mute Stopped reporting" }),
    );
    expect(
      screen.getByRole("button", { name: "Until resolved" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  it("shows a muted alert and lifts its mute", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({ id: 3 })]);
    vi.mocked(api.deleteMute).mockResolvedValue(undefined);

    render(<Line />);
    expect(await screen.findByText("muted permanently")).toBeInTheDocument();
    vi.mocked(api.getMutes).mockResolvedValue([]);
    await userEvent.click(
      screen.getByRole("button", { name: "Unmute Stopped reporting" }),
    );
    expect(api.deleteMute).toHaveBeenCalledWith(3);
    expect(
      await screen.findByRole("button", { name: "Mute Stopped reporting" }),
    ).toBeInTheDocument();
  });

  it("names a wider mute that covers the alert", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([
      mute({ kind: "", subject: null, until: "2026-11-18T14:30:00Z" }),
    ]);
    render(<Line />);
    await waitFor(() =>
      expect(
        screen.getByText(/^muted with all notifications until 18 Nov 2026/),
      ).toBeInTheDocument(),
    );
  });
});
