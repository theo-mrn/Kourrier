import { CaretDown, CircleNotch, WarningCircle, type Icon } from "@phosphor-icons/react";
import type { ButtonHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";

import { ApiError } from "../api/client";
import type { State } from "../api/types";
import { dateTime, timeAgo } from "../lib/format";

export function cx(...classes: (string | false | null | undefined)[]): string {
  return classes.filter(Boolean).join(" ");
}

// ---------- Buttons ----------

type Variant = "primary" | "secondary" | "ghost" | "danger";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-accent text-on-accent hover:bg-accent-hover",
  secondary: "border border-line-strong bg-raised text-ink hover:bg-active",
  ghost: "text-ink-2 hover:bg-hover hover:text-ink",
  danger: "border border-line-strong bg-raised text-critical hover:bg-critical/10",
};

export function Button({
  variant = "secondary",
  size = "md",
  busy = false,
  className,
  children,
  disabled,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: "sm" | "md"; busy?: boolean }) {
  return (
    <button
      type="button"
      {...props}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={cx(
        "inline-flex shrink-0 items-center justify-center gap-2 rounded-lg font-medium whitespace-nowrap",
        "transition-colors duration-200 ease-out-soft active:translate-y-px",
        "disabled:pointer-events-none disabled:opacity-40",
        size === "sm" ? "h-8 px-3 text-xs" : "h-9 px-4 text-sm",
        VARIANTS[variant],
        className,
      )}
    >
      {busy && <CircleNotch aria-hidden className="size-4 animate-spin" />}
      {children}
    </button>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return (
    <kbd className="inline-flex h-5 min-w-5 items-center justify-center rounded border border-line-strong px-1 font-sans text-xs text-ink-3">
      {children}
    </kbd>
  );
}

// ---------- Status ----------

const STATE: Record<State, { label: string; color: string }> = {
  queued: { label: "Queued", color: "var(--neutral)" },
  retrying: { label: "Retrying", color: "var(--warning)" },
  sent: { label: "Sent", color: "var(--good)" },
  failed: { label: "Failed", color: "var(--critical)" },
};

export function stateLabel(state: State): string {
  return STATE[state].label;
}

/**
 * The state of an email as a small circle, as issue statuses in Linear:
 * dashed when waiting, half full when retrying, checked when sent, crossed
 * when failed. Shape and label carry the state, not only the color.
 */
export function StatusIcon({ state, size = 14, label = true }: { state: State; size?: number; label?: boolean }) {
  const { color } = STATE[state];
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 14 14"
      role={label ? "img" : undefined}
      aria-label={label ? STATE[state].label : undefined}
      aria-hidden={label ? undefined : true}
      className="shrink-0"
    >
      {state === "queued" && (
        <circle cx="7" cy="7" r="5.5" fill="none" stroke={color} strokeWidth="1.5" strokeDasharray="2.2 1.8" />
      )}
      {state === "retrying" && (
        <>
          <circle cx="7" cy="7" r="5.5" fill="none" stroke={color} strokeWidth="1.5" />
          <path d="M7 3.5 A3.5 3.5 0 0 1 7 10.5 Z" fill={color} />
        </>
      )}
      {state === "sent" && (
        <>
          <circle cx="7" cy="7" r="6.25" fill={color} />
          <path
            d="M4.4 7.2 6.2 9 9.7 5.3"
            fill="none"
            stroke="var(--panel)"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </>
      )}
      {state === "failed" && (
        <>
          <circle cx="7" cy="7" r="6.25" fill={color} />
          <path d="M5 5 9 9M9 5 5 9" stroke="var(--panel)" strokeWidth="1.6" strokeLinecap="round" />
        </>
      )}
    </svg>
  );
}

/** A state with its icon and label, as a property value. */
export function StatusValue({ state }: { state: State }) {
  return (
    <span className="inline-flex items-center gap-2">
      <StatusIcon state={state} label={false} />
      {STATE[state].label}
    </span>
  );
}

// ---------- Labels ----------

// Neutral dot colors for labels such as tenants: identity, not status.
const DOTS = ["#5e6ad2", "#26b5ce", "#bb87fc", "#f2994a", "#4cb782", "#e573b4"];

function dotFor(name: string): string {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return DOTS[h % DOTS.length] ?? "#5e6ad2";
}

/** A label pill with a colored dot, as Linear's labels. */
export function Label({ children, dot, title }: { children: ReactNode; dot?: string; title?: string }) {
  return (
    <span
      title={title}
      className="inline-flex h-6 items-center gap-2 rounded-full border border-line-strong px-2 text-xs whitespace-nowrap text-ink-2"
    >
      {dot && <span aria-hidden className="size-2 rounded-full" style={{ background: dotFor(dot) }} />}
      {children}
    </span>
  );
}

/** A neutral tag: a locale, a scope, a reason. */
export function Tag({
  children,
  tone = "neutral",
  title,
}: {
  children: ReactNode;
  tone?: "neutral" | "danger";
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cx(
        "inline-flex h-5 items-center rounded px-2 text-xs font-medium whitespace-nowrap",
        tone === "danger" ? "bg-critical/12 text-critical" : "bg-active text-ink-2",
      )}
    >
      {children}
    </span>
  );
}

// ---------- Text ----------

/** A time shown relative to now, with the full date as a tooltip. */
export function Time({ iso }: { iso: string }) {
  return (
    <time dateTime={iso} title={dateTime(iso)} className="tabular whitespace-nowrap">
      {timeAgo(iso)}
    </time>
  );
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cx("font-mono text-xs", className)}>{children}</span>;
}

export function CodeBlock({ children }: { children: string }) {
  return (
    <pre className="max-h-[28rem] overflow-auto rounded-xl border border-line bg-[#111111] p-4 font-mono text-xs leading-5 text-ink-2">
      {children}
    </pre>
  );
}

// ---------- Containers and states ----------

/** A titled block of a page. */
export function Section({
  title,
  actions,
  children,
  className,
}: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={cx("surface-card rounded-xl border border-line-strong", className)}>
      {(title || actions) && (
        <header className="flex h-12 items-center justify-between gap-4 border-b border-line px-5">
          <h2 className="text-sm font-medium">{title}</h2>
          {actions}
        </header>
      )}
      {children}
    </section>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cx("skeleton", className)} />;
}

/** Rows shaped like the list they stand for, while it loads. */
export function ListSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <div role="status" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex h-11 items-center gap-4 border-b border-line px-6">
          <Skeleton className="size-4 rounded-full" />
          <Skeleton className="h-3 w-20" />
          <Skeleton className="h-3 w-48" />
          <Skeleton className="ml-auto h-3 w-16" />
        </div>
      ))}
    </div>
  );
}

export function ErrorNotice({ error }: { error: unknown }) {
  const message = error instanceof ApiError ? error.message : "The console could not reach Kourrier. Try again.";
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-xl border border-critical/30 bg-critical/8 px-4 py-3 text-sm text-ink"
    >
      <WarningCircle aria-hidden weight="fill" className="mt-0.5 size-4 shrink-0 text-critical" />
      {message}
    </div>
  );
}

/** An empty view: what is missing and what to do. */
export function EmptyState({ icon: I, title, children }: { icon: Icon; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center px-6 py-16 text-center">
      <span className="surface-card grid size-11 place-items-center rounded-xl border border-line-strong text-ink-3">
        <I aria-hidden className="size-5" />
      </span>
      <p className="mt-4 text-sm font-medium">{title}</p>
      {children && <div className="mt-1 max-w-sm text-sm text-ink-3">{children}</div>}
    </div>
  );
}

export const inputClass =
  "h-9 w-full rounded-lg border border-line-strong bg-raised px-3 text-sm text-ink placeholder:text-ink-3 " +
  "transition-colors duration-200 ease-out-soft hover:border-ink-3 focus:border-ink-2 focus:outline-none";

/** A select styled like the other controls, with its own caret. */
export function Select({
  className,
  size = "md",
  ...props
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, "size"> & { size?: "sm" | "md" }) {
  return (
    <span className={cx("relative inline-flex", className)}>
      <select
        {...props}
        className={cx(
          inputClass,
          "w-full cursor-pointer appearance-none pr-8",
          size === "sm" ? "h-8 text-xs" : undefined,
        )}
      />
      <CaretDown
        aria-hidden
        className="pointer-events-none absolute top-1/2 right-2.5 size-3 -translate-y-1/2 text-ink-3"
      />
    </span>
  );
}
