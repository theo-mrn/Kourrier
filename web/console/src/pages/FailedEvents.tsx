import { ArrowCounterClockwise, CaretDown, CaretRight, Trash, WarningOctagon } from "@phosphor-icons/react";
import { useState } from "react";

import { useDeadEvent, useDeadEvents, useDeleteDeadEvents, useReplayDeadEvents } from "../api/hooks";
import type { DeadEvent, DeadEventReason, ReplayResponse } from "../api/types";
import { ActionBar } from "../components/ActionBar";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Page } from "../components/Layout";
import {
  Button,
  CodeBlock,
  EmptyState,
  ErrorNotice,
  Label,
  ListSkeleton,
  Skeleton,
  StatusIcon,
  Tag,
  Time,
  cx,
} from "../components/ui";
import { plural } from "../lib/format";
import { useSelection } from "../lib/selection";

const REASONS: Record<DeadEventReason, string> = {
  rejected: "Rejected",
  partial: "Partially failed",
  unrouted: "Unrouted",
  invalid: "Invalid payload",
};

function summary(res: ReplayResponse): string {
  const replayed = res.results.filter((r) => !r.skipped && r.outcome !== "retry");
  const again = replayed.filter((r) => r.dead_letter_id).length;
  const skipped = res.results.filter((r) => r.skipped).length;
  const emails = replayed.reduce((n, r) => n + r.emails, 0);
  const parts = [`${plural(replayed.length, "event")} replayed, ${plural(emails, "email")} queued`];
  if (again) parts.push(`${again} failed again and ${again === 1 ? "was" : "were"} kept`);
  if (skipped) parts.push(`${skipped} skipped`);
  return parts.join(", ") + ".";
}

function Payload({ id }: { id: string }) {
  const { data, error, isPending } = useDeadEvent(id);
  if (isPending) return <Skeleton className="mt-3 h-32 w-full" />;
  if (error) return <ErrorNotice error={error} />;
  return (
    <div className="mt-3">
      <CodeBlock>{data.event ? JSON.stringify(data.event, null, 2) : (data.payload ?? "")}</CodeBlock>
    </div>
  );
}

function Row({ e, selected, onToggle }: { e: DeadEvent; selected: boolean; onToggle: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <li
      className={cx(
        "border-b border-line px-6 py-2.5 text-sm transition-colors duration-200",
        selected ? "bg-accent-soft" : "hover:bg-hover",
      )}
    >
      <div className="flex items-center gap-3">
        <input
          type="checkbox"
          aria-label={`Select event ${e.event_id ?? e.id}`}
          checked={selected}
          onChange={onToggle}
        />
        <button
          type="button"
          aria-expanded={open}
          aria-label={open ? "Hide the event" : "Show the event"}
          onClick={() => setOpen(!open)}
          className="grid size-5 place-items-center rounded text-ink-3 hover:bg-active hover:text-ink"
        >
          {open ? <CaretDown aria-hidden className="size-3" /> : <CaretRight aria-hidden className="size-3" />}
        </button>
        <StatusIcon state="failed" />
        <div className="min-w-0 flex-1">
          <p className="truncate">
            <span className="font-mono text-xs font-medium">{e.event_type ?? "Unreadable payload"}</span>
            {e.event_id && <span className="ml-2 font-mono text-xs text-ink-3">{e.event_id}</span>}
          </p>
          {e.detail && (
            <p className="truncate text-xs text-ink-3" title={e.detail}>
              {e.detail}
            </p>
          )}
        </div>
        <Tag tone={e.reason === "invalid" ? "danger" : "neutral"}>{REASONS[e.reason]}</Tag>
        <span className="hidden shrink-0 items-center gap-3 md:flex">
          <Label dot={e.tenant}>{e.tenant}</Label>
          <span className="w-20 text-right text-xs text-ink-3">
            <Time iso={e.failed_at} />
          </span>
        </span>
      </div>
      {open && (
        <div className="pl-14">
          <p className="mt-2 text-xs text-ink-3">
            From {e.source}
            {e.routes && `, routes ${e.routes.join(", ")}`}
          </p>
          <Payload id={e.id} />
        </div>
      )}
    </li>
  );
}

export function FailedEvents() {
  const query = useDeadEvents();
  const replay = useReplayDeadEvents();
  const remove = useDeleteDeadEvents();
  const entries = query.data?.pages.flatMap((p) => p.entries) ?? [];
  const selection = useSelection(entries.map((e) => e.id));
  const [confirm, setConfirm] = useState<"replay" | "delete" | null>(null);
  const [notice, setNotice] = useState("");
  const disabled = query.data?.pages[0]?.enabled === false;

  function run() {
    const ids = selection.ids;
    if (confirm === "replay") {
      replay.mutate(ids, {
        onSuccess: (res) => {
          setNotice(summary(res) + (res.interrupted ? " Stopped by a transient failure. Run it again later." : ""));
          selection.clear();
        },
        onSettled: () => setConfirm(null),
      });
    } else if (confirm === "delete") {
      remove.mutate(ids, {
        onSuccess: ({ count }) => {
          setNotice(`${plural(count, "event")} deleted.`);
          selection.clear();
        },
        onSettled: () => setConfirm(null),
      });
    }
  }

  const error = replay.error ?? remove.error;

  return (
    <Page
      title="Failed events"
      description="CloudEvents that could not fully become emails. Replaying runs the routes that failed."
      padded={false}
    >
      <p role="status" aria-live="polite" className={cx("px-6 text-sm text-good", notice && "py-3")}>
        {notice}
      </p>
      {error && (
        <div className="px-6 pb-4">
          <ErrorNotice error={error} />
        </div>
      )}
      {query.isPending && <ListSkeleton rows={4} />}
      {query.error && (
        <div className="p-6">
          <ErrorNotice error={query.error} />
        </div>
      )}
      {disabled && (
        <EmptyState icon={WarningOctagon} title="The event ingress is disabled">
          Set KOURRIER_EVENTS_ROUTES_FILE to consume CloudEvents.
        </EmptyState>
      )}
      {query.data && !disabled && entries.length === 0 && (
        <EmptyState icon={WarningOctagon} title="No failed events">
          Every event was routed and turned into emails.
        </EmptyState>
      )}

      {entries.length > 0 && (
        <>
          <div className="flex h-9 items-center gap-3 border-b border-line bg-raised px-6 text-xs font-medium">
            <input
              type="checkbox"
              aria-label="Select every event"
              checked={selection.allSelected}
              onChange={selection.toggleAll}
            />
            Kept for review
            <span className="tabular text-ink-3">{entries.length}</span>
          </div>
          <ul>
            {entries.map((e) => (
              <Row key={e.id} e={e} selected={selection.selected.has(e.id)} onToggle={() => selection.toggle(e.id)} />
            ))}
          </ul>
        </>
      )}

      {query.hasNextPage && (
        <div className="flex justify-center py-4">
          <Button size="sm" variant="ghost" onClick={() => void query.fetchNextPage()} busy={query.isFetchingNextPage}>
            Load more
          </Button>
        </div>
      )}

      <ActionBar count={selection.ids.length} onClear={selection.clear}>
        <Button size="sm" onClick={() => setConfirm("replay")}>
          <ArrowCounterClockwise aria-hidden className="size-4" />
          Replay
        </Button>
        <Button size="sm" variant="danger" onClick={() => setConfirm("delete")}>
          <Trash aria-hidden className="size-4" />
          Delete
        </Button>
      </ActionBar>

      <ConfirmDialog
        open={confirm !== null}
        title={confirm === "delete" ? "Delete these events?" : "Replay these events?"}
        confirmLabel={confirm === "delete" ? "Delete" : "Replay"}
        danger={confirm === "delete"}
        busy={replay.isPending || remove.isPending}
        onConfirm={run}
        onCancel={() => setConfirm(null)}
      >
        {confirm === "delete" ? (
          <p>{plural(selection.ids.length, "event")} will be removed for good. No email will be sent for them.</p>
        ) : (
          <p>
            {plural(selection.ids.length, "event")} will go through the current routes again. An event that fails again
            is kept under a new entry. Invalid payloads are skipped.
          </p>
        )}
      </ConfirmDialog>
    </Page>
  );
}
