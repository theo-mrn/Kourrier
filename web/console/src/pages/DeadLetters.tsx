import { ArrowCounterClockwise, Trash, Tray } from "@phosphor-icons/react";
import { useState } from "react";
import { Link } from "react-router";

import { useDeadLetterAction, useDeadLetters } from "../api/hooks";
import type { DeadJob } from "../api/types";
import { ActionBar } from "../components/ActionBar";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { Page } from "../components/Layout";
import { Button, EmptyState, ErrorNotice, Label, ListSkeleton, StatusIcon, Tag, Time, cx } from "../components/ui";
import { summarizeError } from "../lib/errors";
import { plural, shortID } from "../lib/format";
import { useSelection } from "../lib/selection";

type Action = "replay" | "delete";

function Row({ e, selected, onToggle }: { e: DeadJob; selected: boolean; onToggle: () => void }) {
  return (
    <li
      className={cx(
        "flex items-center gap-3 border-b border-line px-6 py-2.5 text-sm transition-colors duration-200",
        selected ? "bg-accent-soft" : "hover:bg-hover",
      )}
    >
      <input
        type="checkbox"
        aria-label={`Select ${e.template ?? "entry"} ${e.email_id ?? e.id}`}
        checked={selected}
        onChange={onToggle}
      />
      <StatusIcon state="failed" />
      <span className="hidden w-28 shrink-0 font-mono text-xs text-ink-3 sm:block">
        {e.email_id ? shortID(e.email_id) : "unreadable"}
      </span>
      <div className="min-w-0 flex-1">
        <p className="truncate">
          {e.email_id ? (
            <Link to={`/emails/${e.email_id}`} className="font-medium hover:underline">
              {e.template ?? "Inline content"}
            </Link>
          ) : (
            <span className="font-medium">Unreadable job</span>
          )}
          {e.to && <span className="text-ink-3"> to {e.to.join(", ")}</span>}
        </p>
        <p className="truncate font-mono text-xs text-ink-3" title={e.reason}>
          {e.reason}
        </p>
      </div>
      <Tag tone="danger">{summarizeError(e.reason)}</Tag>
      <span className="hidden shrink-0 items-center gap-3 md:flex">
        {e.tenant && <Label dot={e.tenant}>{e.tenant}</Label>}
        <span className="w-20 text-right text-xs text-ink-3">{e.failed_at ? <Time iso={e.failed_at} /> : ""}</span>
      </span>
    </li>
  );
}

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
          action === "replay" ? `${plural(count, "email")} queued again.` : `${plural(count, "entry")} deleted.`,
        );
        selection.clear();
      },
      onSettled: () => setConfirm(null),
    });
  }

  return (
    <Page
      title="Dead letters"
      description="Emails that failed for good. Fix the cause, then replay them."
      padded={false}
    >
      <p role="status" aria-live="polite" className={cx("px-6 text-sm text-good", notice && "py-3")}>
        {notice}
      </p>
      {mutation.error && (
        <div className="px-6 pb-4">
          <ErrorNotice error={mutation.error} />
        </div>
      )}
      {query.isPending && <ListSkeleton rows={4} />}
      {query.error && (
        <div className="p-6">
          <ErrorNotice error={query.error} />
        </div>
      )}
      {query.data && entries.length === 0 && (
        <EmptyState icon={Tray} title="No dead letters">
          Every email was delivered or is still being retried.
        </EmptyState>
      )}

      {entries.length > 0 && (
        <>
          <div className="flex h-9 items-center gap-3 border-b border-line bg-raised px-6 text-xs font-medium">
            <input
              type="checkbox"
              aria-label="Select every entry"
              checked={selection.allSelected}
              onChange={selection.toggleAll}
            />
            <StatusIcon state="failed" label={false} />
            Failed for good
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
        <Button size="sm" disabled={readable === 0} onClick={() => setConfirm("replay")}>
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
        title={confirm === "delete" ? "Delete these entries?" : "Replay these emails?"}
        confirmLabel={confirm === "delete" ? "Delete" : "Replay"}
        danger={confirm === "delete"}
        busy={mutation.isPending}
        onConfirm={run}
        onCancel={() => setConfirm(null)}
      >
        {confirm === "delete" ? (
          <p>{plural(selection.ids.length, "entry")} will be removed for good. These emails will not be sent.</p>
        ) : (
          <p>
            {plural(readable, "email")} will be queued again with a fresh attempt counter. Fix the cause first, or they
            will fail again.
            {readable < selection.ids.length && " Unreadable entries are skipped."}
          </p>
        )}
      </ConfirmDialog>
    </Page>
  );
}
