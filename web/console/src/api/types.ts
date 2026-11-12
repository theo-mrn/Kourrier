// Types of the console admin API (internal/console). Keep them in sync with
// the Go structs: the field names are their JSON tags.

export type State = "queued" | "retrying" | "sent" | "failed";
export const STATES: readonly State[] = ["queued", "retrying", "sent", "failed"];

export interface Session {
  expires_at: string;
}

export interface Overview {
  version: string;
  mode: string;
  mailer: string;
  storage: string;
  started_at: string;
  queue: { ready: number; pending: number; delayed: number; dead: number };
  tenants: number;
  activity: boolean;
  events: { routes: number; sources: number; dead: number } | null;
}

export interface ActivityEntry {
  id: string;
  email_id: string;
  tenant: string;
  state: State;
  template?: string;
  to?: string[];
  origin?: string;
  attempt?: number;
  error?: string;
  at: string;
}

export interface ActivityPage {
  enabled: boolean;
  entries: ActivityEntry[];
  next?: string;
}

export interface EmailStatus {
  id: string;
  status: State;
  attempts: number;
  provider?: string;
  provider_message_id?: string;
  last_error?: string;
  next_attempt_at?: string;
  created_at: string;
  updated_at: string;
}

export interface EmailDetail {
  tenant?: string;
  status: EmailStatus | null;
  activity: ActivityEntry[];
}

export interface DeadJob {
  id: string;
  email_id?: string;
  tenant?: string;
  template?: string;
  to?: string[];
  attempts: number;
  reason: string;
  failed_at: string;
  readable: boolean;
}

export interface DLQPage {
  entries: DeadJob[];
  next?: string;
}

export type DeadEventReason = "rejected" | "partial" | "unrouted" | "invalid";

export interface DeadEvent {
  id: string;
  tenant: string;
  source: string;
  reason: DeadEventReason;
  detail?: string;
  routes?: string[];
  failed_at: string;
  replayable: boolean;
  event_type?: string;
  event_id?: string;
  event?: unknown;
  payload?: string;
}

export interface DeadEventsPage {
  enabled: boolean;
  entries: DeadEvent[];
  next?: string;
}

export interface ReplayResult {
  entry_id: string;
  skipped?: string;
  outcome?: "accepted" | "ignored" | "rejected" | "retry";
  emails: number;
  dead_letter_id?: string;
}

export interface ReplayResponse {
  results: ReplayResult[];
  interrupted?: string;
}

export interface Count {
  count: number;
}

export interface Address {
  email: string;
  name?: string;
}

export interface Tenant {
  id: string;
  default_sender: Address;
  allowed_senders: string[];
  api_keys: { scopes: string[] }[];
}

export interface TemplateInfo {
  name: string;
  locales: string[];
}

export interface TenantTemplates {
  tenant: string;
  templates: TemplateInfo[];
  error?: string;
}

export interface Preview {
  locale: string;
  subject: string;
  html?: string;
  text?: string;
}

export interface RouteInfo {
  name: string;
  tenant: string;
  template: string;
  types: string[];
  source?: string;
  when?: string;
}

export interface NATSSource {
  name: string;
  tenant: string;
  stream: string;
  consumer: string;
  filter_subjects?: string[];
  bind: boolean;
  on_unrouted?: string;
  deliver_policy?: string;
}

export interface Routes {
  enabled: boolean;
  routes: RouteInfo[];
  sources: NATSSource[];
}

/** RFC 9457 problem returned on errors. */
export interface Problem {
  type: string;
  title: string;
  status: number;
  detail?: string;
}

export type StatsRange = "1h" | "24h";

export interface StatsBucket {
  start: string;
  sent: number;
  failed: number;
}

export interface Breakdown {
  name: string;
  sent: number;
  failed: number;
}

export interface Stats {
  enabled: boolean;
  range: StatsRange;
  from?: string;
  to?: string;
  step_seconds?: number;
  complete?: boolean;
  totals?: { queued: number; sent: number; failed: number; retries: number };
  buckets?: StatsBucket[];
  latency?: { p50_ms: number; p95_ms: number } | null;
  templates?: Breakdown[];
  tenants?: Breakdown[];
}
