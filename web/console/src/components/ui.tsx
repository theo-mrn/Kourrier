import { AlertTriangle, Loader2 } from "lucide-react";
import type { ButtonHTMLAttributes, ReactNode } from "react";

import { ApiError } from "../api/client";
import type { State } from "../api/types";
import { dateTime, timeAgo } from "../lib/format";

export function cx(...classes: (string | false | null | undefined)[]): string {
  return classes.filter(Boolean).join(" ");
}

type Variant = "primary" | "secondary" | "danger" | "ghost";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-ink text-card hover:bg-[var(--p-ink-700)] border-ink",
  secondary: "bg-card text-ink hover:bg-sunken border-line-strong",
  danger: "bg-card text-failed hover:bg-failed-soft border-failed",
  ghost: "bg-transparent text-ink-2 hover:bg-sunken hover:text-ink border-transparent",
};

export function Button({
  variant = "secondary",
  busy = false,
  className,
  children,
  disabled,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; busy?: boolean }) {
  return (
    <button
      type="button"
      {...props}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-m border px-3.5 py-2 text-sm font-semibold",
        "transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        VARIANTS[variant],
        className,
      )}
    >
      {busy && <Loader2 aria-hidden className="size-4 animate-spin" />}
      {children}
    </button>
  );
}

export function Card({ className, children }: { className?: string; children: ReactNode }) {
  return <section className={cx("rounded-l border border-line bg-card", className)}>{children}</section>;
}

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h1 className="display text-3xl font-bold sm:text-4xl">{title}</h1>
        {description && <p className="mt-1.5 max-w-2xl text-ink-2">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </header>
  );
}

const STATE_INK: Record<State, string> = {
  queued: "text-queued",
  retrying: "text-retrying",
  sent: "text-sent",
  failed: "text-failed",
};

/** A status as a postmark: the same state always has the same ink. */
export function StatusStamp({ state, large = false }: { state: State; large?: boolean }) {
  return (
    <span
      className={cx(
        "stamp-type inline-block rounded-s border-[1.5px] border-current leading-none whitespace-nowrap",
        large ? "-rotate-2 px-2.5 py-1.5 text-base" : "px-1.5 py-1 text-[11px]",
        STATE_INK[state],
      )}
    >
      {state}
    </span>
  );
}

/** A neutral stamp for reasons and labels. */
export function Tag({ children, tone = "neutral" }: { children: ReactNode; tone?: "neutral" | "red" | "blue" }) {
  const ink = {
    neutral: "text-ink-2 border-line-strong",
    red: "text-failed border-current",
    blue: "text-queued border-current",
  }[tone];
  return (
    <span
      className={cx(
        "stamp-type inline-block rounded-s border px-1.5 py-1 text-[11px] leading-none whitespace-nowrap",
        ink,
      )}
    >
      {children}
    </span>
  );
}

/** A time shown relative to now, with the full date as a tooltip. */
export function Time({ iso }: { iso: string }) {
  return (
    <time dateTime={iso} title={dateTime(iso)} className="tabular whitespace-nowrap">
      {timeAgo(iso)}
    </time>
  );
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return <span className={cx("font-mono text-[13px]", className)}>{children}</span>;
}

/** Code on ink paper, as on the landing page. */
export function CodeBlock({ children, label }: { children: string; label?: string }) {
  return (
    <div className="overflow-hidden rounded-l bg-code">
      {label && <div className="border-b border-code-line px-4 py-2 font-mono text-xs text-code-dim">{label}</div>}
      <pre className="max-h-[28rem] overflow-auto p-4 font-mono text-[13px] leading-relaxed text-code-text">
        {children}
      </pre>
    </div>
  );
}

export function Loading({ label = "Loading" }: { label?: string }) {
  return (
    <p role="status" className="flex items-center gap-2 py-10 text-ink-3">
      <Loader2 aria-hidden className="size-4 animate-spin" />
      {label}…
    </p>
  );
}

export function ErrorNotice({ error }: { error: unknown }) {
  const message = error instanceof ApiError ? error.message : "The console could not reach Kourrier.";
  return (
    <div role="alert" className="flex items-start gap-3 rounded-l border border-failed bg-failed-soft/40 p-4 text-sm">
      <AlertTriangle aria-hidden className="mt-0.5 size-4 shrink-0 text-failed" />
      <p className="text-ink">{message}</p>
    </div>
  );
}

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="rounded-l border border-dashed border-line-strong px-6 py-12 text-center">
      <p className="display text-xl font-semibold">{title}</p>
      {children && <div className="mx-auto mt-2 max-w-md text-sm text-ink-2">{children}</div>}
    </div>
  );
}

/** A labelled form control. */
export function Field({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  return (
    <label className={cx("flex flex-col gap-1.5 text-sm", className)}>
      <span className="font-mono text-xs tracking-wide text-ink-3 uppercase">{label}</span>
      {children}
    </label>
  );
}

export const inputClass =
  "w-full rounded-m border border-line-strong bg-card px-3 py-2 text-sm text-ink placeholder:text-ink-3 " +
  "focus:border-blue focus:outline-none";

/** Table styles shared by the list pages. */
export const table = {
  wrap: "overflow-x-auto rounded-l border border-line bg-card",
  table: "w-full min-w-[720px] border-collapse text-left text-sm [&_tbody_tr:last-child_td]:border-b-0",
  th: "border-b border-line bg-sunken/60 px-4 py-2.5 font-mono text-[11px] font-medium uppercase tracking-wide text-ink-3",
  td: "border-b border-line px-4 py-3 align-top",
};
