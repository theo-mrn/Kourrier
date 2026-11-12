import type { Problem } from "./types";

/** An error answered by the admin API, with its RFC 9457 problem. */
export class ApiError extends Error {
  readonly status: number;
  readonly type: string;

  constructor(problem: Problem) {
    super(problem.detail ?? problem.title);
    this.name = "ApiError";
    this.status = problem.status;
    this.type = problem.type;
  }

  get unauthorized(): boolean {
    return this.status === 401;
  }
}

type Query = Record<string, string | number | undefined>;

/** Builds an API URL, dropping empty parameters. */
export function apiURL(path: string, query: Query = {}): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return `/api${path}${qs ? `?${qs}` : ""}`;
}

async function request<T>(method: string, path: string, body?: unknown, query?: Query): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET") {
    // Required by the server on every write (CSRF protection, ADR 016).
    headers["X-Kourrier-Console"] = "1";
  }
  if (body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(apiURL(path, query), {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return undefined as T;

  const data: unknown = await res.json().catch(() => undefined);
  if (!res.ok) {
    const problem = (data ?? {}) as Partial<Problem>;
    throw new ApiError({
      type: problem.type ?? "unknown",
      title: problem.title ?? res.statusText,
      status: res.status,
      detail: problem.detail,
    });
  }
  return data as T;
}

export const api = {
  get: <T>(path: string, query?: Query) => request<T>("GET", path, undefined, query),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  delete: <T>(path: string) => request<T>("DELETE", path),
};
