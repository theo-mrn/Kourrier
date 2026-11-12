import { useEffect, useRef, useState } from "react";

import type { Breakdown, StatsBucket } from "../api/types";
import { count } from "../lib/format";

/*
 * Hand-drawn SVG charts, following the dataviz rules: thin columns with a
 * 4px rounded data end and a square baseline, a 2px gap between stacked
 * segments, hairline gridlines, a legend for two series, a tooltip per
 * column and a table view. Sent and failed use the validated pair
 * --series-sent / --series-failed.
 */

/** Width of an element, kept up to date. */
function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => entry && setWidth(entry.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, width] as const;
}

/** A clean axis maximum and its ticks: 0, step, 2 step… */
function niceScale(max: number): { top: number; ticks: number[] } {
  if (max <= 0) return { top: 4, ticks: [0, 2, 4] };
  const rough = max / 3;
  const pow = 10 ** Math.floor(Math.log10(rough));
  const step = [1, 2, 5, 10].map((m) => m * pow).find((s) => s >= rough) ?? 10 * pow;
  const top = Math.ceil(max / step) * step;
  const ticks: number[] = [];
  for (let v = 0; v <= top; v += step) ticks.push(v);
  return { top, ticks };
}

/** A rectangle with its top corners rounded: the data end of a column. */
function roundedTop(x: number, y: number, w: number, h: number, r: number): string {
  const rr = Math.min(r, w / 2, h);
  return `M${x},${y + h}V${y + rr}Q${x},${y} ${x + rr},${y}H${x + w - rr}Q${x + w},${y} ${x + w},${y + rr}V${y + h}Z`;
}

const clock = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit" });

export function Legend() {
  return (
    <div className="flex items-center gap-4 text-xs text-ink-2">
      <span className="inline-flex items-center gap-2">
        <span aria-hidden className="size-2.5 rounded-sm bg-series-sent" /> Sent
      </span>
      <span className="inline-flex items-center gap-2">
        <span aria-hidden className="size-2.5 rounded-sm bg-series-failed" /> Failed
      </span>
    </div>
  );
}

/** Emails sent and failed per bucket, as stacked columns. */
export function ThroughputChart({ buckets, stepSeconds }: { buckets: StatsBucket[]; stepSeconds: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const height = 200;
  const pad = { left: 36, right: 0, top: 8, bottom: 24 };
  const plotW = Math.max(width - pad.left - pad.right, 0);
  const plotH = height - pad.top - pad.bottom;
  const max = Math.max(0, ...buckets.map((b) => b.sent + b.failed));
  const { top, ticks } = niceScale(max);
  const slot = buckets.length ? plotW / buckets.length : 0;
  const barW = Math.max(Math.min(24, slot - 2), 1);
  const y = (v: number) => pad.top + plotH - (v / top) * plotH;
  // About six time labels whatever the range.
  const every = Math.max(1, Math.round(buckets.length / 6));
  const hovered = hover === null ? null : buckets[hover];

  return (
    <div ref={ref} className="relative" onPointerLeave={() => setHover(null)}>
      {width > 0 && (
        <svg width={width} height={height} role="img" aria-label="Emails sent and failed over time">
          {ticks.map((t) => (
            <g key={t}>
              <line x1={pad.left} x2={width} y1={y(t)} y2={y(t)} stroke="var(--chart-grid)" strokeWidth="1" />
              <text
                x={pad.left - 8}
                y={y(t)}
                dy="0.32em"
                textAnchor="end"
                className="tabular"
                fontSize="11"
                fill="var(--text-3)"
              >
                {count(t)}
              </text>
            </g>
          ))}
          {buckets.map((b, i) => {
            const x = pad.left + i * slot + (slot - barW) / 2;
            const sentH = (b.sent / top) * plotH;
            const failedH = (b.failed / top) * plotH;
            const gap = b.sent > 0 && b.failed > 0 ? 2 : 0;
            const dim = hover !== null && hover !== i;
            return (
              <g key={b.start} opacity={dim ? 0.45 : 1} style={{ transition: "opacity 200ms" }}>
                {b.sent > 0 && (
                  <path
                    d={
                      b.failed > 0
                        ? `M${x},${y(0)}V${y(0) - sentH}H${x + barW}V${y(0)}Z`
                        : roundedTop(x, y(0) - sentH, barW, sentH, 4)
                    }
                    fill="var(--series-sent)"
                  />
                )}
                {b.failed > 0 && (
                  <path d={roundedTop(x, y(0) - sentH - gap - failedH, barW, failedH, 4)} fill="var(--series-failed)" />
                )}
                {i % every === 0 && (
                  <text x={x + barW / 2} y={height - 6} textAnchor="middle" fontSize="11" fill="var(--text-3)">
                    {clock.format(new Date(b.start))}
                  </text>
                )}
                {/* The hit target is the whole slot, taller than the bar. */}
                <rect
                  x={pad.left + i * slot}
                  y={pad.top}
                  width={slot}
                  height={plotH}
                  fill="transparent"
                  onPointerEnter={() => setHover(i)}
                />
              </g>
            );
          })}
        </svg>
      )}
      {hovered && hover !== null && (
        <div
          role="tooltip"
          className="pointer-events-none absolute top-0 z-10 w-44 rounded-lg bg-overlay p-3 text-xs shadow-float"
          style={{
            left: Math.min(Math.max(pad.left + hover * slot + slot / 2 - 88, 0), Math.max(width - 176, 0)),
          }}
        >
          <p className="text-ink-3">
            {clock.format(new Date(hovered.start))} to{" "}
            {clock.format(new Date(new Date(hovered.start).getTime() + stepSeconds * 1000))}
          </p>
          <p className="mt-2 flex items-center gap-2">
            <span aria-hidden className="h-0.5 w-3 rounded bg-series-sent" />
            <span className="tabular font-medium text-ink">{count(hovered.sent)}</span>
            <span className="text-ink-3">sent</span>
          </p>
          <p className="mt-1 flex items-center gap-2">
            <span aria-hidden className="h-0.5 w-3 rounded bg-series-failed" />
            <span className="tabular font-medium text-ink">{count(hovered.failed)}</span>
            <span className="text-ink-3">failed</span>
          </p>
        </div>
      )}
    </div>
  );
}

/** The same data as a table: the view that never depends on hovering. */
export function ThroughputTable({ buckets, stepSeconds }: { buckets: StatsBucket[]; stepSeconds: number }) {
  const rows = buckets.filter((b) => b.sent + b.failed > 0);
  if (rows.length === 0) return <p className="px-4 py-6 text-sm text-ink-3">No email in this window.</p>;
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="text-left text-xs text-ink-3">
          <th scope="col" className="px-4 py-2 font-medium">
            Period
          </th>
          <th scope="col" className="px-4 py-2 text-right font-medium">
            Sent
          </th>
          <th scope="col" className="px-4 py-2 text-right font-medium">
            Failed
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((b) => (
          <tr key={b.start} className="border-t border-line">
            <td className="px-4 py-2 text-ink-2">
              {clock.format(new Date(b.start))} to{" "}
              {clock.format(new Date(new Date(b.start).getTime() + stepSeconds * 1000))}
            </td>
            <td className="tabular px-4 py-2 text-right">{count(b.sent)}</td>
            <td className="tabular px-4 py-2 text-right">{count(b.failed)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A 12 point trend: the line in the quiet gray, the latest point in the accent. */
export function Sparkline({ values, label }: { values: number[]; label: string }) {
  const n = 12;
  const size = Math.ceil(values.length / n) || 1;
  const points: number[] = [];
  for (let i = 0; i < values.length; i += size) points.push(values.slice(i, i + size).reduce((a, b) => a + b, 0));
  const w = 88;
  const h = 28;
  const max = Math.max(1, ...points);
  const xy = points.map((v, i) => [
    points.length > 1 ? (i / (points.length - 1)) * (w - 8) + 4 : w / 2,
    h - 4 - (v / max) * (h - 8),
  ]);
  const last = xy[xy.length - 1];
  if (!last) return null;
  return (
    <svg width={w} height={h} role="img" aria-label={label} className="overflow-visible">
      <polyline
        points={xy.map(([x, y]) => `${x},${y}`).join(" ")}
        fill="none"
        stroke="var(--text-3)"
        strokeWidth="2"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
      <circle cx={last[0]} cy={last[1]} r="4" fill="var(--accent)" stroke="var(--raised)" strokeWidth="2" />
    </svg>
  );
}

/** Sent and failed per item, as thin horizontal bars sharing one scale. */
export function BreakdownBars({ items }: { items: Breakdown[] }) {
  const max = Math.max(1, ...items.map((i) => i.sent + i.failed));
  return (
    <ul className="flex flex-col gap-3">
      {items.map((i) => (
        <li key={i.name}>
          <div className="flex items-baseline justify-between gap-4 text-sm">
            <span className="truncate">{i.name}</span>
            <span className="tabular shrink-0 text-xs text-ink-3">
              {count(i.sent)} sent{i.failed > 0 && `, ${count(i.failed)} failed`}
            </span>
          </div>
          <div className="mt-2 flex h-1.5 gap-0.5" aria-hidden>
            {i.sent > 0 && (
              <span
                className="rounded-l-full bg-series-sent last:rounded-r-full"
                style={{ width: `${(i.sent / max) * 100}%` }}
              />
            )}
            {i.failed > 0 && (
              <span
                className="rounded-r-full bg-series-failed first:rounded-l-full"
                style={{ width: `${(i.failed / max) * 100}%` }}
              />
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}
