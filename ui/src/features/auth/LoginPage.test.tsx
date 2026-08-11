import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LoginPage } from "./LoginPage";

const fetchMock = vi.fn();

beforeEach(() => {
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

// The hub answers POST /login with a 303 to "/" on success; fetch follows it,
// so what the page sees is an ok response from the redirect target.
function ok() {
  return new Response("", { status: 200 });
}

async function submit(token: string) {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Admin token"), token);
  await user.click(screen.getByRole("button", { name: "Log in" }));
}

describe("LoginPage", () => {
  it("posts form-encoded to /login, the way session.go parses it", async () => {
    fetchMock.mockResolvedValue(ok());
    render(<LoginPage />);

    await submit("s3cret");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/login");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("same-origin");
    expect(init.body).toBeInstanceOf(URLSearchParams);
    expect((init.body as URLSearchParams).get("token")).toBe("s3cret");
  });

  it("never sends an Authorization header or stores the token", async () => {
    fetchMock.mockResolvedValue(ok());
    render(<LoginPage />);

    await submit("s3cret");

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers ?? {});
    expect(headers.has("Authorization")).toBe(false);
    expect(JSON.stringify(Object.entries(localStorage))).not.toContain(
      "s3cret",
    );
  });

  it("calls onSuccess when the hub accepts the token", async () => {
    fetchMock.mockResolvedValue(ok());
    const onSuccess = vi.fn();
    render(<LoginPage onSuccess={onSuccess} />);

    await submit("s3cret");

    expect(onSuccess).toHaveBeenCalledTimes(1);
  });

  it("reports a rejected token without echoing it back", async () => {
    fetchMock.mockResolvedValue(new Response("<html>", { status: 401 }));
    const onSuccess = vi.fn();
    render(<LoginPage onSuccess={onSuccess} />);

    await submit("wrong");

    expect(
      await screen.findByText("That is not the admin token."),
    ).toBeInTheDocument();
    expect(onSuccess).not.toHaveBeenCalled();
    // The rejected value must not reappear in the field: a wrong token here
    // is often a right token somewhere else.
    expect(screen.getByLabelText("Admin token")).toHaveValue("");
  });

  it("reports a transport failure separately from a rejection", async () => {
    fetchMock.mockRejectedValue(new TypeError("network"));
    render(<LoginPage />);

    await submit("s3cret");

    expect(
      await screen.findByText(/could not reach the hub/i),
    ).toBeInTheDocument();
  });

  it("keeps the native form post as the no-JS fallback", () => {
    render(<LoginPage />);
    const form = screen.getByRole("form", { name: "Admin login" });
    expect(form).toHaveAttribute("action", "/login");
    expect(form.getAttribute("method")?.toLowerCase()).toBe("post");
    expect(screen.getByLabelText("Admin token")).toHaveAttribute(
      "name",
      "token",
    );
  });
});
