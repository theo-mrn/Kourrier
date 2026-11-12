import {
  MutationCache,
  QueryCache,
  QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

import { api, ApiError } from "./client";
import type {
  ActivityPage,
  Count,
  DLQPage,
  DeadEvent,
  DeadEventsPage,
  EmailDetail,
  Overview,
  Preview,
  ReplayResponse,
  Routes,
  Session,
  State,
  Stats,
  StatsRange,
  Tenant,
  TenantTemplates,
} from "./types";

const SESSION = ["session"] as const;

/**
 * The query client of the console. A 401 on any request means the session
 * expired: the session query is reset, which shows the sign-in screen.
 */
export function createQueryClient(): QueryClient {
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({ onError: (err) => onUnauthorized(err) }),
    mutationCache: new MutationCache({ onError: (err) => onUnauthorized(err) }),
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        refetchOnWindowFocus: true,
        // Client errors will not go away by retrying.
        retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
      },
    },
  });
  function onUnauthorized(err: unknown) {
    if (err instanceof ApiError && err.unauthorized) {
      client.setQueryData(SESSION, null);
    }
  }
  return client;
}

/** The current session, or null when signed out. */
export function useSession() {
  return useQuery({
    queryKey: SESSION,
    queryFn: async () => {
      try {
        return await api.get<Session>("/session");
      } catch (err) {
        if (err instanceof ApiError && err.unauthorized) return null;
        throw err;
      }
    },
    retry: false,
    staleTime: Infinity,
  });
}

export function useSignIn() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (token: string) => api.post<Session>("/session", { token }),
    onSuccess: (session) => {
      client.removeQueries({ predicate: (q) => q.queryKey[0] !== SESSION[0] });
      client.setQueryData(SESSION, session);
    },
  });
}

export function useSignOut() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () => api.delete<undefined>("/session"),
    onSettled: () => {
      client.clear();
      client.setQueryData(SESSION, null);
    },
  });
}

/** Refresh period of the live views. */
export const LIVE_INTERVAL = 5_000;

export function useOverview() {
  return useQuery({
    queryKey: ["overview"],
    queryFn: () => api.get<Overview>("/overview"),
    refetchInterval: LIVE_INTERVAL,
  });
}

export function useStats(range: StatsRange) {
  return useQuery({
    queryKey: ["stats", range],
    queryFn: () => api.get<Stats>("/stats", { range }),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
  });
}

/** The latest failures, for the dashboard. */
export function useRecentFailures() {
  return useQuery({
    queryKey: ["activity", "failures"],
    queryFn: () => api.get<ActivityPage>("/activity", { state: "failed", limit: 5 }),
    refetchInterval: LIVE_INTERVAL,
  });
}

export interface ActivityFilters {
  tenant?: string;
  state?: State | "";
  email?: string;
}

export function useActivity(filters: ActivityFilters, live: boolean) {
  return useInfiniteQuery({
    queryKey: ["activity", filters],
    queryFn: ({ pageParam }) => api.get<ActivityPage>("/activity", { ...filters, before: pageParam, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
    refetchInterval: live ? LIVE_INTERVAL : false,
  });
}

export function useEmail(id: string) {
  return useQuery({
    queryKey: ["email", id],
    queryFn: () => api.get<EmailDetail>(`/emails/${encodeURIComponent(id)}`),
    refetchInterval: LIVE_INTERVAL,
  });
}

export function useDeadLetters() {
  return useInfiniteQuery({
    queryKey: ["dlq"],
    queryFn: ({ pageParam }) => api.get<DLQPage>("/dlq", { after: pageParam, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });
}

/** Replays or deletes entries of the e-mail dead-letter queue. */
export function useDeadLetterAction(action: "replay" | "delete") {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) => api.post<Count>(`/dlq/${action}`, { ids }),
    onSettled: () => {
      void client.invalidateQueries({ queryKey: ["dlq"] });
      void client.invalidateQueries({ queryKey: ["overview"] });
    },
  });
}

export function useDeadEvents() {
  return useInfiniteQuery({
    queryKey: ["dead-events"],
    queryFn: ({ pageParam }) => api.get<DeadEventsPage>("/dead-events", { after: pageParam, limit: 50 }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });
}

export function useDeadEvent(id: string | undefined) {
  return useQuery({
    queryKey: ["dead-event", id],
    queryFn: () => api.get<DeadEvent>(`/dead-events/${encodeURIComponent(id ?? "")}`),
    enabled: Boolean(id),
  });
}

export function useReplayDeadEvents() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) => api.post<ReplayResponse>("/dead-events/replay", { ids }),
    onSettled: () => invalidateDeadEvents(client),
  });
}

export function useDeleteDeadEvents() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) => api.post<Count>("/dead-events/delete", { ids }),
    onSettled: () => invalidateDeadEvents(client),
  });
}

function invalidateDeadEvents(client: QueryClient) {
  void client.invalidateQueries({ queryKey: ["dead-events"] });
  void client.invalidateQueries({ queryKey: ["dead-event"] });
  void client.invalidateQueries({ queryKey: ["overview"] });
}

export function useTenants() {
  return useQuery({ queryKey: ["tenants"], queryFn: () => api.get<Tenant[]>("/tenants"), staleTime: 60_000 });
}

export function useTemplates() {
  return useQuery({ queryKey: ["templates"], queryFn: () => api.get<TenantTemplates[]>("/templates") });
}

export interface PreviewRequest {
  tenant: string;
  name: string;
  locale: string;
  /** JSON object, as typed by the operator. */
  data: string;
}

/** Query parameters of a preview, shared by the JSON and HTML endpoints. */
export function previewQuery(req: PreviewRequest) {
  return { locale: req.locale, data: req.data.trim() };
}

export function previewPath(req: PreviewRequest, html = false) {
  const base = `/templates/${encodeURIComponent(req.tenant)}/${encodeURIComponent(req.name)}/preview`;
  return html ? `${base}.html` : base;
}

export function usePreview(req: PreviewRequest | null) {
  return useQuery({
    queryKey: ["preview", req],
    queryFn: () => api.get<Preview>(previewPath(req as PreviewRequest), previewQuery(req as PreviewRequest)),
    enabled: req !== null,
    staleTime: 0,
  });
}

export function useRoutes() {
  return useQuery({ queryKey: ["routes"], queryFn: () => api.get<Routes>("/routes"), staleTime: 60_000 });
}
