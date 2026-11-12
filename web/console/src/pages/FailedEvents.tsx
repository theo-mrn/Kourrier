import { Eye, RotateCcw, Trash2, X } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { useDeadEvent, useDeadEvents, useDeleteDeadEvents, useReplayDeadEvents } from "../api/hooks";
import type { DeadEventReason, ReplayResponse } from "../api/types";
import { ConfirmDialog } from "../components/ConfirmDialog";
import {
  Button,
  CodeBlock,
  EmptyState,
  ErrorNotice,
  Loading,
  Mono,
  PageHeader,
  Tag,
  Time,
  table,
} from "../components/ui";
import { plural } from "../lib/format";
import { useSelection } from "../lib/selection";

const REASONS: Record<DeadEventReason, string> = {
  rejected: "Every matching route failed.",
  partial: "Some routes failed; only those are replayed.",
  unrouted: "No route matched, on a source that keeps unrouted events.",
  invalid: "The payload is not a valid CloudEvent: it cannot be replayed.",
};

function summary(res: ReplayResponse): string {
  const replayed = res.results.filter((r) => !r.skipped && r.outcome !== "retry");
  const again = replayed.filter((r) => r.dead_letter_id).length;
  const skipped = res.results.filter((r) => r.skipped).length;
  const parts = [
    `${plural(replayed.length, "event")} replayed, ${plural(
      replayed.reduce((n, r) => n + r.emails, 0),
      "e-mail",
    )} queued`,
  ];
  if (again) parts.push(`${again} failed again and ${again === 1 ? "was" : "were"} kept`);
  if (skipped) parts.push(`${skipped} skipped`);
  return parts.join(", ") + ".";
}

/** The kept event, in a drawer over the list. Escape closes it. */
function EventDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { data, error, isPending } = useDeadEvent(id);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <aside
      aria-label="Event"
      className="fixed inset-y-0 right-0 z-40 flex w-full max-w-xl flex-col border-l border-line bg-card shadow-2xl"
    >
      <div className="airmail h-1.5 shrink-0" aria-hidden />
      <div className="overflow-y-auto p-6">
        <div className="mb-4 flex items-start justify-between gap-4">
          <div>
            <h2 className="display text-xl font-semibold">Event</h2>
            <Mono className="text-ink-3">{id}</Mono>
          </div>
          <Button variant="ghost" onClick={onClose} aria-label="Close the event">
            <X aria-hidden className="size-4" />
          </Button>
        </div>
        {isPending && <Loading />}
        {error && <ErrorNotice error={error} />}
        {data && (
          <>
            <p className="mb-2 text-sm text-ink-2">{REASONS[data.reason]}</p>
            {data.detail && (
              <p className="mb-4 font-mono text-[13px] break-words whitespace-pre-wrap text-failed">{data.detail}</p>
            )}
            <CodeBlock label={data.event ? "CloudEvent (structured mode)" : "Raw payload"}>
              {data.event ? JSON.stringify(data.event, null, 2) : (data.payload ?? "")}
            </CodeBlock>
          </>
        )}
      </div>
    </aside>
  );
}

export function FailedEvents() {
  const query = useDeadEvents();
  const replay = useReplayDeadEvents();
  const remove = useDeleteDeadEvents();
  const entries = query.data?.pages.flatMap((p) => p.entries) ?? [];
  const selection = useSelection(entries.map((e) => e.id));
  const [confirm, setConfirm] = useState<"replay" | "delete" | null>(null);
  const [shown, setShown] = useState<string>();
  const [notice, setNotice] = useState("");
  const disabled = query.data?.pages[0]?.enabled === false;
  const closeDetail = useCallback(() => setShown(undefined), []);

  function run() {
    const ids = selection.ids;
    if (confirm === "replay") {
      replay.mutate(ids, {
        onSuccess: (res) => {
          setNotice(summary(res) + (res.interrupted ? " Stopped by a transient failure: run it again later." : ""));
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
    <>
      <PageHeader
        title="Failed events"
        description="CloudEvents the ingress could not fully turn into e-mails. Replaying runs the current routes again, only those that failed."
        actions={
          !disabled && (
            <>
              <Button disabled={selection.ids.length === 0} onClick={() => setConfirm("replay")}>
                <RotateCcw aria-hidden className="size-4" />
                Replay{selection.ids.length ? ` (${selection.ids.length})` : ""}
              </Button>
              <Button variant="danger" disabled={selection.ids.length === 0} onClick={() => setConfirm("delete")}>
                <Trash2 aria-hidden className="size-4" />
                Delete
              </Button>
            </>
          )
        }
      />

      <p role="status" aria-live="polite" className="mb-3 min-h-5 text-sm font-medium text-sent">
        {notice}
      </p>
      {error && <ErrorNotice error={error} />}
      {query.isPending && <Loading />}
      {query.error && <ErrorNotice error={query.error} />}
      {disabled && (
        <EmptyState title="The event ingress is disabled">
          Configure a routes file (<code className="font-mono">KOURRIER_EVENTS_ROUTES_FILE</code>) to consume
          CloudEvents.
        </EmptyState>
      )}
      {query.data && !disabled && entries.length === 0 && (
        <EmptyState title="No failed event">Every event was routed and turned into e-mails.</EmptyState>
      )}

      {entries.length > 0 && (
        <div className={table.wrap}>
          <table className={table.table}>
            <thead>
              <tr>
                <th scope="col" className={`${table.th} w-10`}>
                  <input
                    type="checkbox"
                    aria-label="Select every event"
                    checked={selection.allSelected}
                    onChange={selection.toggleAll}
                  />
                </th>
                <th scope="col" className={table.th}>
                  Failed
                </th>
                <th scope="col" className={table.th}>
                  Reason
                </th>
                <th scope="col" className={table.th}>
                  Tenant
                </th>
                <th scope="col" className={table.th}>
                  Event
                </th>
                <th scope="col" className={table.th}>
                  Source
                </th>
                <th scope="col" className={table.th}>
                  <span className="sr-only">Details</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr
                  key={e.id}
                  className={selection.selected.has(e.id) || shown === e.id ? "bg-blue-soft/40" : "hover:bg-page/60"}
                >
                  <td className={table.td}>
                    <input
                      type="checkbox"
                      aria-label={`Select event ${e.event_id ?? e.id}`}
                      checked={selection.selected.has(e.id)}
                      onChange={() => selection.toggle(e.id)}
                    />
                  </td>
                  <td className={table.td}>
                    <Time iso={e.failed_at} />
                  </td>
                  <td className={table.td}>
                    <Tag tone={e.reason === "invalid" ? "red" : "neutral"}>{e.reason}</Tag>
                    {e.routes && <p className="mt-1.5 text-xs text-ink-3">routes: {e.routes.join(", ")}</p>}
                  </td>
                  <td className={table.td}>{e.tenant}</td>
                  <td className={table.td}>
                    <Mono>{e.event_type ?? "unreadable"}</Mono>
                    {e.event_id && <p className="font-mono text-xs text-ink-3">{e.event_id}</p>}
                  </td>
                  <td className={table.td}>{e.source}</td>
                  <td className={`${table.td} text-right`}>
                    <Button
                      variant="ghost"
                      onClick={() => setShown(e.id)}
                      aria-label={`Show event ${e.event_id ?? e.id}`}
                    >
                      <Eye aria-hidden className="size-4" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {shown && <EventDetail id={shown} onClose={closeDetail} />}

      {query.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button onClick={() => void query.fetchNextPage()} busy={query.isFetchingNextPage}>
            Load more
          </Button>
        </div>
      )}

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
          <p>{plural(selection.ids.length, "event")} will be removed for good. No e-mail will be sent for them.</p>
        ) : (
          <p>
            {plural(selection.ids.length, "event")} will go through the current routes again. An event that fails again
            is kept under a new entry; invalid payloads are skipped.
          </p>
        )}
      </ConfirmDialog>
    </>
  );
}
