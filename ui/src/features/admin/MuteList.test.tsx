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
import { MuteList } from "./MuteList";

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

beforeEach(() => {
  vi.mocked(api.getMutes).mockReset();
  vi.mocked(api.deleteMute).mockReset();
});

describe("MuteList", () => {
  it("renders nothing when nothing is muted", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([]);
    const { container } = render(<MuteList />);
    await waitFor(() => expect(api.getMutes).toHaveBeenCalled());
    expect(container).toBeEmptyDOMElement();
  });

  it("lists the fleet's mutes and lifts one", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([
      mute({ id: 1 }),
      mute({ id: 2, host_id: 8, hostname: "web-02", kind: "silent" }),
      mute({ id: 3, kind: "disk", subject: "/var", until_resolved: true }),
    ]);
    vi.mocked(api.deleteMute).mockResolvedValue(undefined);

    render(<MuteList />);
    expect(
      await screen.findByRole("heading", { name: "Muted notifications" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "web-02" })).toHaveAttribute(
      "href",
      "/hosts/8/overview",
    );
    expect(
      screen.getByText("Stopped reporting permanently"),
    ).toBeInTheDocument();
    // Two disk mutes on one host read apart by their subject.
    expect(
      screen.getByText("Filesystem nearly full — /var until resolved"),
    ).toBeInTheDocument();

    vi.mocked(api.getMutes).mockResolvedValue([mute({ id: 1 })]);
    await userEvent.click(
      screen.getByRole("button", {
        name: "Unmute Stopped reporting on web-02",
      }),
    );
    expect(api.deleteMute).toHaveBeenCalledWith(2);
    await waitFor(() =>
      expect(screen.queryByText("web-02")).not.toBeInTheDocument(),
    );
  });

  it("says when the mutes could not be read instead of looking empty", async () => {
    vi.mocked(api.getMutes).mockRejectedValue(new Error("internal error"));
    render(<MuteList />);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Mutes could not be loaded: internal error",
    );
  });

  it("re-reads when the page says the mutes changed", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({})]);
    const { rerender } = render(<MuteList version={1} />);
    await screen.findByText("kessel");
    vi.mocked(api.getMutes).mockResolvedValue([]);
    rerender(<MuteList version={2} />);
    await waitFor(() =>
      expect(screen.queryByText("kessel")).not.toBeInTheDocument(),
    );
  });

  it("says why an unmute failed", async () => {
    vi.mocked(api.getMutes).mockResolvedValue([mute({})]);
    vi.mocked(api.deleteMute).mockRejectedValue(new Error("internal error"));

    render(<MuteList />);
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Unmute all notifications on kessel",
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "internal error",
    );
  });
});
