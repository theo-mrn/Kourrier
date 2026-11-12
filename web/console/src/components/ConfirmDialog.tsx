import { useEffect, useRef, type ReactNode } from "react";

import { Button } from "./ui";

/**
 * A modal confirmation on the native <dialog> element: focus is trapped,
 * Escape cancels and the page behind is inert.
 */
export function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel,
  danger = false,
  busy = false,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal?.();
    if (!open && dialog.open) dialog.close?.();
  }, [open]);

  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        onCancel();
      }}
      aria-labelledby="confirm-title"
      className="animate-pop m-auto w-[min(26rem,calc(100vw-2rem))] rounded-2xl border border-line-strong bg-overlay p-0 text-ink shadow-float"
    >
      <div className="p-5">
        <h2 id="confirm-title" className="text-base font-medium">
          {title}
        </h2>
        <div className="mt-2 text-sm text-ink-2">{children}</div>
      </div>
      <div className="flex justify-end gap-2 border-t border-line px-5 py-3">
        <Button variant="ghost" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
        <Button variant={danger ? "danger" : "primary"} onClick={onConfirm} busy={busy} autoFocus>
          {confirmLabel}
        </Button>
      </div>
    </dialog>
  );
}
