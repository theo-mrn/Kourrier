import { describe, expect, it } from "vitest";

import { mockFetch } from "../test/fetch";
import { ApiError, api, apiURL } from "./client";

describe("apiURL", () => {
  it("drops empty parameters", () => {
    expect(apiURL("/activity", { tenant: "shop", state: "", before: undefined, limit: 50 })).toBe(
      "/api/activity?tenant=shop&limit=50",
    );
  });
});

describe("api", () => {
  it("sends the CSRF header on writes only", async () => {
    const fetch = mockFetch({
      "GET /api/overview": () => ({ body: {} }),
      "POST /api/dlq/replay": () => ({ body: { count: 2 } }),
    });
    await api.get("/overview");
    await expect(api.post("/dlq/replay", { ids: ["1-0"] })).resolves.toEqual({ count: 2 });

    const [get, post] = fetch.mock.calls.map(([, init]) => init);
    expect((get?.headers as Record<string, string>)["X-Kourrier-Console"]).toBeUndefined();
    expect((post?.headers as Record<string, string>)["X-Kourrier-Console"]).toBe("1");
    expect(post?.credentials).toBe("same-origin");
    expect(post?.body).toBe('{"ids":["1-0"]}');
  });

  it("turns problems into ApiError", async () => {
    mockFetch({
      "GET /api/overview": () => ({
        status: 401,
        body: { type: "unauthorized", title: "Unauthorized", status: 401, detail: "sign in" },
      }),
    });
    const err = await api.get("/overview").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).unauthorized).toBe(true);
    expect((err as ApiError).message).toBe("sign in");
  });

  it("accepts empty responses", async () => {
    mockFetch({ "DELETE /api/session": () => ({ status: 204 }) });
    await expect(api.delete("/session")).resolves.toBeUndefined();
  });
});
