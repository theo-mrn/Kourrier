import { vi } from "vitest";

type Handler = (init: RequestInit | undefined, url: URL) => { status?: number; body?: unknown };

/**
 * Replaces fetch with routes keyed by "METHOD /path" (query string ignored).
 * Unknown routes answer 404, so a test fails loudly on an unexpected call.
 */
export function mockFetch(routes: Record<string, Handler>) {
  const spy = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://console.test");
    const key = `${init?.method ?? "GET"} ${url.pathname}`;
    const handler = routes[key];
    const { status = 200, body } = handler
      ? handler(init, url)
      : { status: 404, body: { type: "not-found", title: "Not Found", status: 404 } };
    return new Response(status === 204 ? null : JSON.stringify(body ?? {}), {
      status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", spy);
  return spy;
}
