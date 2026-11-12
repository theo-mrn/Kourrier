import { RotateCcw, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";

import { useDeadLetterAction, useDeadLetters } from "../api/hooks";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Button, EmptyState, ErrorNotice, Loading, Mono, PageHeader, Tag, Time, table } from "../components/ui";
import { plural, shortID } from "../lib/format";
import { useSelection } from "../lib/selection";

type Action = "replay" | "delete";

export function DeadLetters() {
  const query = useDeadLetters();
  const replay = useDeadLetterAction("replay");
  const remove = useDeadLetterAction("delete");
  const entries = query.data?.pages.flatMap((p) => p.entries) ?? [];
  const selection = useSelection(entries.map((e) => e.id));
  const [confirm, setConfirm] = useState<Action | null>(null);
  const [notice, setNotice] = useState("");

  const mutation = confirm === "delete" ? remove : replay;
  const readable = entries.filter((e) => selection.selected.has(e.id) && e.readable).length;

  function run() {
    if (!confirm) return;
    const action = confirm;
    mutation.mutate(selection.ids, {
      onSuccess: ({ count }) => {
        setNotice(
          action === "replay" ? `${plural(count, "job")} back in the queue.` : `${plural(count, "job")} deleted.`,
        );
        selection.clear();
      },
      onSettled: () => setConfirm(null),
    });
  }

  return (
    <>
      <PageHeader
        title="Dead letters"
        description="E-mails that failed for good: a permanent error, or every attempt used. Replaying puts them back in the queue with a fresh attempt counter."
        actions={
          <>
            <Button disabled={readable === 0} onClick={() => setConfirm("replay")}>
              <RotateCcw aria-hidden className="size-4" />
              Replay{selection.ids.length ? ` (${selection.ids.length})` : ""}
            </Button>
            <Button variant="danger" disabled={selection.ids.length === 0} onClick={() => setConfirm("delete")}>
              <Trash2 aria-hidden className="size-4" />
              Delete
            </Button>
          </>
        }
      />

      <p role="status" aria-live="polite" className="mb-3 min-h-5 text-sm font-medium text-sent">
        {notice}
      </p>
      {mutation.error && <ErrorNotice error={mutation.error} />}
      {query.isPending && <Loading />}
      {query.error && <ErrorNotice error={query.error} />}
      {query.data && entries.length === 0 && (
        <EmptyState title="The dead-letter queue is empty">No e-mail failed for good. Nothing to do.</EmptyState>
      )}

      {entries.length > 0 && (
        <div className={table.wrap}>
          <table className={table.table}>
            <thead>
              <tr>
                <th scope="col" className={`${table.th} w-10`}>
                  <input
                    type="checkbox"
                    aria-label="Select every entry"
                    checked={selection.allSelected}
                    onChange={selection.toggleAll}
                  />
                </th>
                <th scope="col" className={table.th}>
                  Failed
                </th>
                <th scope="col" className={table.th}>
                  E-mail
                </th>
                <th scope="col" className={table.th}>
                  Tenant
                </th>
                <th scope="col" className={table.th}>
                  Template
                </th>
                <th scope="col" className={table.th}>
                  To
                </th>
                <th scope="col" className={table.th}>
                  Attempts
                </th>
                <th scope="col" className={table.th}>
                  Reason
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id} className={selection.selected.has(e.id) ? "bg-blue-soft/40" : "hover:bg-page/60"}>
                  <td className={table.td}>
                    <input
                      type="checkbox"
                      aria-label={`Select ${e.email_id ?? e.id}`}
                      checked={selection.selected.has(e.id)}
                      onChange={() => selection.toggle(e.id)}
                    />
                  </td>
                  <td className={table.td}>{e.failed_at ? <Time iso={e.failed_at} /> : "?"}</td>
                  <td className={table.td}>
                    {e.email_id ? (
                      <Link to={`/emails/${e.email_id}`} className="text-blue hover:underline">
                        <Mono>{shortID(e.email_id)}</Mono>
                      </Link>
                    ) : (
                      <Tag tone="red">unreadable</Tag>
                    )}
                  </td>
                  <td className={table.td}>{e.tenant}</td>
                  <td className={table.td}>{e.template ?? <span className="text-ink-3">inline</span>}</td>
                  <td className={table.td}>
                    <Mono className="text-ink-2">{e.to?.join(", ")}</Mono>
                  </td>
                  <td className={`${table.td} tabular`}>{e.attempts}</td>
                  <td className={`${table.td} max-w-sm font-mono text-[13px] break-words text-failed`}>{e.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {query.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button onClick={() => void query.fetchNextPage()} busy={query.isFetchingNextPage}>
            Load more
          </Button>
        </div>
      )}

      <ConfirmDialog
        open={confirm !== null}
        title={confirm === "delete" ? "Delete these e-mails?" : "Replay these e-mails?"}
        confirmLabel={confirm === "delete" ? "Delete" : "Replay"}
        danger={confirm === "delete"}
        busy={mutation.isPending}
        onConfirm={run}
        onCancel={() => setConfirm(null)}
      >
        {confirm === "delete" ? (
          <p>{plural(selection.ids.length, "entry")} will be removed for good. The e-mails will not be sent.</p>
        ) : (
          <p>
            {plural(readable, "e-mail")} will be queued again and sent by the workers. Fix the cause first (template,
            sender, provider), or they will fail again.
            {readable < selection.ids.length && " Unreadable entries are skipped."}
          </p>
        )}
      </ConfirmDialog>
    </>
  );
}
