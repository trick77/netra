import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../lib/api", async () => ({
  ...(await vi.importActual<typeof import("../../lib/api")>("../../lib/api")),
  getMutes: vi.fn(),
  deleteMute: vi.fn(),
}));

import * as api from "../../lib/api";
import type { Mute } from "../../lib/api";
import { HostMutes } from "./HostMutes";
import { useMutes } from "./useMutes";

const mute = (over: Partial<Mute>): Mute => ({
  id: 1,
  host_id: 7,
  hostname: "kessel",
  kind: "",
  subject: null,
  until_resolved: false,
  until: null,
  note: "",
  created: "2026-11-18T10:00:00Z",
  ...over,
});

function Header() {
  return <HostMutes mutes={useMutes(7)} />;
}

beforeEach(() => {
  vi.mocked(api.getMutes).mockReset();
  vi.mocked(api.deleteMute).mockReset();
});

describe("HostMutes", () => {
  it("renders nothing when nothing is muted", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    const { container } = render(<Header />);
    await waitFor(() => expect(api.getMutes).toHaveBeenCalledWith(7));
    expect(container).toBeEmptyDOMElement();
  });

  it("lists this host's mutes and lifts one", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([
      mute({ id: 1, kind: "silent", subject: "" }),
    ]);
    vi.mocked(api.deleteMute).mockResolvedValue(undefined);

    render(<Header />);
    expect(
      await screen.findByText(/Muted: Stopped reporting permanently/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("group", { name: "Notification mutes" }),
    ).toBeInTheDocument();

    vi.mocked(api.getMutes).mockResolvedValue([]);
    await userEvent.click(
      screen.getByRole("button", { name: "Unmute Stopped reporting" }),
    );
    expect(api.deleteMute).toHaveBeenCalledWith(1);
    await waitFor(() =>
      expect(screen.queryByText(/Muted:/)).not.toBeInTheDocument(),
    );
  });

  it("says when the mutes could not be read", async () => {
    vi.mocked(api.getMutes).mockRejectedValue(new Error("internal error"));
    render(<Header />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Mutes could not be loaded: internal error",
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

    render(<Header />);
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

    finishFirst();
    await waitFor(() => expect(first).not.toBeInTheDocument());
    expect(second).toBeDisabled();
  });

  it("says why an unmute failed, and keeps the row", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({})]);
    vi.mocked(api.deleteMute).mockRejectedValue(
      new api.ApiError(500, "internal error"),
    );

    render(<Header />);
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

    render(<Header />);
    await userEvent.click(
      await screen.findByRole("button", { name: "Unmute all notifications" }),
    );
    await waitFor(() =>
      expect(screen.queryByText(/Muted:/)).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
