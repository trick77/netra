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
import {
  MUTABLE_EVENT_TYPES,
  MuteControl,
  muteScope,
  muteText,
  muteUntil,
} from "./MuteControl";

const mute = (over: Partial<Mute>): Mute => ({
  id: 1,
  host_id: 7,
  hostname: "kessel",
  kind: "",
  until: null,
  note: "",
  created: "2026-11-18T10:00:00Z",
  ...over,
});

beforeEach(() => {
  vi.mocked(api.getMutes).mockReset();
  vi.mocked(api.createMute).mockReset();
  vi.mocked(api.deleteMute).mockReset();
});

describe("mute wording", () => {
  it("names the scope, the end and the note", () => {
    expect(muteScope("")).toBe("all notifications");
    expect(muteScope("disk")).toBe("Filesystem nearly full");
    expect(muteScope("disk_error")).toBe("disk_error");
    expect(muteUntil(mute({}))).toBe("until lifted");
    expect(muteUntil(mute({ until: "2026-11-18T14:30:00Z" }))).toMatch(
      /^until 18 Nov 2026/,
    );
    expect(muteText(mute({ kind: "load", note: "batch job" }))).toBe(
      "Load above normal until lifted (batch job)",
    );
  });

  it("offers every event type the hub accepts", () => {
    expect(MUTABLE_EVENT_TYPES).toContain("mdraid");
    expect(MUTABLE_EVENT_TYPES).toContain("hub");
    expect(MUTABLE_EVENT_TYPES).toContain("disk_error");
  });
});

describe("MuteControl", () => {
  it("reads this host's mutes and lifts one", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({ id: 1, kind: "disk" })]);
    vi.mocked(api.deleteMute).mockResolvedValue(undefined);

    render(<MuteControl hostId={7} />);
    expect(
      await screen.findByText(/Muted: Filesystem nearly full until lifted/),
    ).toBeInTheDocument();
    expect(api.getMutes).toHaveBeenCalledWith(7);
    expect(
      screen.getByRole("group", { name: "Notification mutes" }),
    ).toBeInTheDocument();

    vi.mocked(api.getMutes).mockResolvedValue([]);
    await userEvent.click(
      screen.getByRole("button", { name: "Unmute Filesystem nearly full" }),
    );
    expect(api.deleteMute).toHaveBeenCalledWith(1);
    await waitFor(() =>
      expect(screen.queryByText(/Muted:/)).not.toBeInTheDocument(),
    );
  });

  it("says when the mutes could not be read", async () => {
    vi.mocked(api.getMutes).mockRejectedValue(new Error("internal error"));
    render(<MuteControl hostId={7} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Mutes could not be loaded: internal error",
    );
  });

  it("mutes a kind for a day, with a note", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    vi.mocked(api.createMute).mockResolvedValue(mute({ kind: "mdraid" }));

    render(<MuteControl hostId={7} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute notifications" }),
    );
    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: "What to mute" }),
      "mdraid",
    );
    await userEvent.click(screen.getByRole("button", { name: "24 h" }));
    await userEvent.type(
      screen.getByRole("textbox", { name: "Why (optional)" }),
      " rebuilding ",
    );
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));

    expect(api.createMute).toHaveBeenCalledWith(
      7,
      "mdraid",
      1440,
      "rebuilding",
    );
    expect(
      await screen.findByRole("button", { name: "Mute notifications" }),
    ).toBeInTheDocument();
  });

  it("says why a mute failed, and Cancel starts over", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    vi.mocked(api.createMute).mockRejectedValue(
      new Error("that host does not exist"),
    );

    render(<MuteControl hostId={7} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute notifications" }),
    );
    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: "What to mute" }),
      "load",
    );
    await userEvent.click(screen.getByRole("button", { name: "Until lifted" }));
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "that host does not exist",
    );
    expect(api.createMute).toHaveBeenCalledWith(7, "load", 0, "");

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Mute notifications" }),
    );
    expect(screen.getByRole("combobox", { name: "What to mute" })).toHaveValue(
      "",
    );
    expect(screen.getByRole("button", { name: "1 h" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("keeps each lift's spinner while several are in flight", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([
      mute({ id: 1 }),
      mute({ id: 2, kind: "load" }),
    ]);
    let finishFirst: () => void = () => {};
    vi.mocked(api.deleteMute).mockImplementation(
      (id) =>
        new Promise<void>((resolve) => {
          if (id === 1) finishFirst = resolve;
          // The second never finishes within the test.
        }),
    );

    render(<MuteControl hostId={7} />);
    const first = await screen.findByRole("button", {
      name: "Unmute all notifications",
    });
    const second = screen.getByRole("button", {
      name: "Unmute Load above normal",
    });
    await userEvent.click(first);
    await userEvent.click(second);
    expect(first).toBeDisabled();
    expect(second).toBeDisabled();

    // The first is lifted: its row goes at once, the second keeps spinning.
    finishFirst();
    await waitFor(() => expect(first).not.toBeInTheDocument());
    expect(second).toBeDisabled();
  });

  it("says why an unmute failed, and keeps the row", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({})]);
    vi.mocked(api.deleteMute).mockRejectedValue(
      new api.ApiError(500, "internal error"),
    );

    render(<MuteControl hostId={7} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Unmute all notifications" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "internal error",
    );
    expect(screen.getByText(/Muted: all notifications/)).toBeInTheDocument();
  });

  it("treats a mute that is already gone as lifted", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({})]);
    vi.mocked(api.deleteMute).mockRejectedValue(
      new api.ApiError(404, "not found"),
    );

    render(<MuteControl hostId={7} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Unmute all notifications" }),
    );
    await waitFor(() =>
      expect(screen.queryByText(/Muted:/)).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("refuses a note the hub would refuse, counting bytes", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    render(<MuteControl hostId={7} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Mute notifications" }),
    );
    // 300 characters, 600 bytes: under maxLength, over the hub's limit.
    const note = screen.getByRole("textbox", { name: "Why (optional)" });
    await userEvent.click(note);
    await userEvent.paste("ä".repeat(300));
    await userEvent.click(screen.getByRole("button", { name: "Mute" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "longer than 500 bytes",
    );
    expect(api.createMute).not.toHaveBeenCalled();
    expect(screen.getByRole("group", { name: "Mute for" })).toBeInTheDocument();
  });
});
