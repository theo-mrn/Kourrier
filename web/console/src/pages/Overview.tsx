import { ArrowRight, ChartBar } from "@phosphor-icons/react";
import { useState, type ReactNode } from "react";
import { Link } from "react-router";

import { useOverview, useRecentFailures, useStats } from "../api/hooks";
import type { StatsRange } from "../api/types";
import { BreakdownBars, Legend, Sparkline, ThroughputChart, ThroughputTable } from "../components/charts";
import { Page } from "../components/Layout";
import { EmptyState, ErrorNotice, Section, Skeleton, StatusIcon, Time, cx } from "../components/ui";
import { summarizeError } from "../lib/errors";
import { count } from "../lib/format";

const RANGES: { value: StatsRange; label: string }[] = [
  { value: "1h", label: "1 hour" },
  { value: "24h", label: "24 hours" },
];

function RangePicker({ value, onChange }: { value: StatsRange; onChange: (r: StatsRange) => void }) {
  return (
    <div role="radiogroup" aria-label="Time range" className="flex rounded-lg border border-line p-0.5">
      {RANGES.map((r) => (
        <button
          key={r.value}
          type="button"
          role="radio"
          aria-checked={value === r.value}
          onClick={() => onChange(r.value)}
          className={cx(
            "h-7 rounded-md px-3 text-xs transition-colors duration-200",
            value === r.value ? "bg-active text-ink" : "text-ink-3 hover:text-ink",
          )}
        >
          {r.label}
        </button>
      ))}
    </div>
  );
}

function Kpi({ label, value, sub, trend }: { label: string; value: string; sub: string; trend?: ReactNode }) {
  return (
    <div className="surface-card rounded-xl border border-line-strong p-5">
      <p className="text-xs text-ink-3">{label}</p>
      <div className="mt-2 flex items-end justify-between gap-4">
        <div className="min-w-0">
          <p className="tabular text-2xl font-medium tracking-tight">{value}</p>
          <p className="mt-1 truncate text-xs whitespace-nowrap text-ink-3">{sub}</p>
        </div>
        {trend}
      </div>
    </div>
  );
}

function duration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}

function QueueRow({ label, value, icon, to }: { label: string; value: number; icon: ReactNode; to?: string }) {
  const body = (
    <>
      {icon}
      <span className="flex-1 text-sm text-ink-2">{label}</span>
      <span className={cx("tabular text-sm font-medium", to && value > 0 && "text-critical")}>{count(value)}</span>
    </>
  );
  return to && value > 0 ? (
    <Link to={to} className="flex h-10 items-center gap-3 px-4 transition-colors duration-200 hover:bg-hover">
      {body}
    </Link>
  ) : (
    <div className="flex h-10 items-center gap-3 px-4">{body}</div>
  );
}

export function Overview() {
  const [range, setRange] = useState<StatsRange>("1h");
  const stats = useStats(range);
  const overview = useOverview();
  const failures = useRecentFailures();
  const [asTable, setAsTable] = useState(false);

  const s = stats.data;
  const o = overview.data;
  const totals = s?.totals;
  const buckets = s?.buckets ?? [];
  const done = totals ? totals.sent + totals.failed : 0;
  const window = range === "1h" ? "last hour" : "last 24 hours";

  return (
    <Page title="Dashboard" actions={<RangePicker value={range} onChange={setRange} />}>
      {(stats.error || overview.error) && <ErrorNotice error={stats.error ?? overview.error} />}

      {stats.isPending && (
        <div role="status" aria-label="Loading" className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-24 rounded-lg" />
          ))}
        </div>
      )}

      {s && !s.enabled && (
        <EmptyState icon={ChartBar} title="No activity feed">
          The dashboard is computed from the activity feed. Set KOURRIER_ACTIVITY_MAX_LEN above 0 to enable it.
        </EmptyState>
      )}

      {s?.enabled && totals && (
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <Kpi
              label="Sent"
              value={count(totals.sent)}
              sub={`Delivered, ${window}`}
              trend={<Sparkline values={buckets.map((b) => b.sent)} label="Sent over time" />}
            />
            <Kpi
              label="Failed"
              value={count(totals.failed)}
              sub={totals.retries ? `${count(totals.retries)} retries along the way` : "No retry needed"}
              trend={<Sparkline values={buckets.map((b) => b.failed)} label="Failures over time" />}
            />
            <Kpi
              label="Success rate"
              value={done ? `${((totals.sent / done) * 100).toFixed(1)} %` : "No data"}
              sub={`${count(done)} emails settled`}
            />
            <Kpi
              label="Delivery time, p95"
              value={s.latency ? duration(s.latency.p95_ms) : "No data"}
              sub={s.latency ? `Median ${duration(s.latency.p50_ms)}, from queued to sent` : "From queued to sent"}
            />
          </div>

          <Section
            title="Emails over time"
            actions={
              <div className="flex items-center gap-4">
                <Legend />
                <button
                  type="button"
                  onClick={() => setAsTable(!asTable)}
                  aria-pressed={asTable}
                  className="text-xs text-ink-3 hover:text-ink"
                >
                  {asTable ? "Show the chart" : "Show as a table"}
                </button>
              </div>
            }
          >
            <div className={asTable ? "max-h-80 overflow-y-auto" : "px-4 pt-4 pb-2"}>
              {asTable ? (
                <ThroughputTable buckets={buckets} stepSeconds={s.step_seconds ?? 60} />
              ) : (
                <ThroughputChart buckets={buckets} stepSeconds={s.step_seconds ?? 60} />
              )}
            </div>
            {!s.complete && (
              <p className="border-t border-line px-4 py-2 text-xs text-ink-3">
                The activity feed does not reach back to the start of this window: older emails are not counted.
              </p>
            )}
          </Section>

          <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
            <Section title="Queue now">
              {o && (
                <div className="py-1">
                  <QueueRow label="Ready" value={o.queue.ready} icon={<StatusIcon state="queued" label={false} />} />
                  <QueueRow
                    label="In progress"
                    value={o.queue.pending}
                    icon={<StatusIcon state="retrying" label={false} />}
                  />
                  <QueueRow
                    label="Waiting for a retry"
                    value={o.queue.delayed}
                    icon={<StatusIcon state="retrying" label={false} />}
                  />
                  <QueueRow
                    label="Dead letters"
                    value={o.queue.dead}
                    to="/dead-letters"
                    icon={<StatusIcon state="failed" label={false} />}
                  />
                  {o.events && (
                    <QueueRow
                      label="Failed events"
                      value={o.events.dead}
                      to="/failed-events"
                      icon={<StatusIcon state="failed" label={false} />}
                    />
                  )}
                </div>
              )}
            </Section>

            <Section title="Top templates">
              <div className="p-4">
                {s.templates?.length ? (
                  <BreakdownBars items={s.templates} />
                ) : (
                  <p className="text-sm text-ink-3">No email in this window.</p>
                )}
              </div>
            </Section>

            <Section
              title="Latest failures"
              actions={
                <Link
                  to="/activity?state=failed"
                  className="inline-flex items-center gap-1 text-xs text-ink-3 hover:text-ink"
                >
                  All <ArrowRight aria-hidden className="size-3" />
                </Link>
              }
            >
              {failures.data?.entries.length ? (
                <ul className="py-1">
                  {failures.data.entries.map((e) => (
                    <li key={e.id}>
                      <Link
                        to={`/emails/${e.email_id}`}
                        className="flex items-center gap-3 px-4 py-2 transition-colors duration-200 hover:bg-hover"
                      >
                        <StatusIcon state="failed" label={false} />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-sm">{e.template ?? "Inline content"}</span>
                          <span className="block truncate text-xs text-ink-3">
                            {e.error ? summarizeError(e.error) : "Failed"} · {e.tenant}
                          </span>
                        </span>
                        <span className="text-xs text-ink-3">
                          <Time iso={e.at} />
                        </span>
                      </Link>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="p-4 text-sm text-ink-3">No failure recently.</p>
              )}
            </Section>
          </div>
        </div>
      )}
    </Page>
  );
}
