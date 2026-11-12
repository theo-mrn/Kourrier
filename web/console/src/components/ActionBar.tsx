import { X } from "@phosphor-icons/react";
import type { ReactNode } from "react";

/** Floats at the bottom of the screen while rows are selected, as in Linear. */
export function ActionBar({ count, onClear, children }: { count: number; onClear: () => void; children: ReactNode }) {
  if (count === 0) return null;
  return (
    <div
      role="toolbar"
      aria-label="Selection"
      className="animate-slide-up fixed bottom-6 left-1/2 z-30 flex -translate-x-1/2 items-center gap-2 rounded-2xl border border-line-strong bg-overlay py-2 pr-2 pl-4 shadow-float"
    >
      <span className="text-sm whitespace-nowrap">
        <span className="tabular font-medium">{count}</span> selected
      </span>
      <button
        type="button"
        onClick={onClear}
        aria-label="Clear the selection"
        className="grid size-7 place-items-center rounded-md text-ink-3 hover:bg-hover hover:text-ink"
      >
        <X aria-hidden className="size-4" />
      </button>
      <span aria-hidden className="mx-1 h-5 w-px bg-line-strong" />
      {children}
    </div>
  );
}
