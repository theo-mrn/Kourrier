import { QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";

import { createQueryClient } from "./api/hooks";
import type { Overview } from "./api/types";
import { App } from "./App";
import { mockFetch } from "./test/fetch";

const overview: Overview = {
  version: "v0.2.0",
  mode: "all",
  mailer: "console",
  storage: "filesystem",
  started_at: "2026-11-12T10:00:00Z",
  queue: { ready: 3, pending: 1, delayed: 2, dead: 7 },
  tenants: 1,
  activity: true,
  events: null,
};

const stats = {
  enabled: true,
  range: "1h",
  step_seconds: 60,
  complete: true,
  totals: { queued: 12, sent: 11, failed: 1, retries: 0 },
  buckets: [{ start: "2026-11-12T10:00:00Z", sent: 11, failed: 1 }],
  latency: { p50_ms: 180, p95_ms: 420 },
  templates: [{ name: "welcome", sent: 11, failed: 1 }],
  tenants: [{ name: "shop", sent: 11, failed: 1 }],
};

const unauthorized = { status: 401, body: { type: "unauthorized", title: "Unauthorized", status: 401 } };

function renderApp() {
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter>
        <App />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("App", () => {
  it("asks for the token, signs in and shows the overview", async () => {
    let signedIn = false;
    const fetch = mockFetch({
      "GET /api/session": () => (signedIn ? { body: { expires_at: "2026-11-13T00:00:00Z" } } : unauthorized),
      "POST /api/session": (init) => {
        signedIn = JSON.parse(String(init?.body)).token === "the-admin-token";
        return signedIn ? { body: { expires_at: "2026-11-13T00:00:00Z" } } : unauthorized;
      },
      "GET /api/overview": () => (signedIn ? { body: overview } : unauthorized),
      "GET /api/stats": () => ({ body: stats }),
      "GET /api/activity": () => ({ body: { enabled: true, entries: [] } }),
    });
    const user = userEvent.setup();
    renderApp();

    const input = await screen.findByLabelText("Admin token");
    await user.type(input, "wrong");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("This token is not valid.");

    await user.clear(input);
    await user.type(input, "the-admin-token");
    await user.click(screen.getByRole("button", { name: "Continue" }));

    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();
    // The dead letters: in the queue panel, and as a counter in the menu.
    expect(await screen.findByText("91.7 %")).toBeInTheDocument();
    expect(await screen.findAllByText("7")).toHaveLength(2);
    const nav = screen.getByRole("navigation", { name: "Console" });
    expect(within(nav).getByRole("link", { name: /Dead letters/ })).toHaveTextContent("7 waiting");
    expect(fetch).toHaveBeenCalledWith("/api/session", expect.objectContaining({ method: "POST" }));
  });

  it("goes back to the sign-in screen when the session expires", async () => {
    let expired = false;
    mockFetch({
      "GET /api/session": () => (expired ? unauthorized : { body: { expires_at: "2026-11-13T00:00:00Z" } }),
      "GET /api/overview": () => (expired ? unauthorized : { body: overview }),
      "GET /api/activity": () => unauthorized,
      "GET /api/tenants": () => unauthorized,
    });
    renderApp();
    expect(await screen.findByRole("heading", { name: "Dashboard" })).toBeInTheDocument();

    expired = true;
    await userEvent.setup().click(screen.getByRole("link", { name: "Emails" }));
    expect(await screen.findByLabelText("Admin token", {}, { timeout: 3000 })).toBeInTheDocument();
  });
});
