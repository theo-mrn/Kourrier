import { EnvelopeSimple } from "@phosphor-icons/react";
import { Link, useSearchParams } from "react-router";

import { useActivity, useTenants } from "../api/hooks";
import { STATES, type ActivityEntry, type State } from "../api/types";
import { Page } from "../components/Layout";
import {
  Button,
  EmptyState,
  ErrorNotice,
  Label,
  ListSkeleton,
  StatusIcon,
  Tag,
  Time,
  Select,
  stateLabel,
} from "../components/ui";
import { summarizeError } from "../lib/errors";
import { shortID } from "../lib/format";

/**
 * The feed records every status change; the list shows each email once, with
 * its latest change. Entries come newest first, so the first one wins.
 */
function latestPerEmail(entries: ActivityEntry[]): ActivityEntry[] {
  const seen = new Set<string>();
  return entries.filter((e) => !seen.has(e.email_id) && seen.add(e.email_id));
}

// Groups in the order an operator reads them: problems first.
const GROUPS: State[] = ["failed", "retrying", "queued", "sent"];

function Row({ e }: { e: ActivityEntry }) {
  return (
    <li>
      <Link
        to={`/emails/${e.email_id}`}
        className="flex h-11 items-center gap-3 border-b border-line px-6 text-sm transition-colors duration-200 hover:bg-hover"
      >
        <StatusIcon state={e.state} />
        <span className="hidden w-28 shrink-0 font-mono text-xs text-ink-3 sm:block">{shortID(e.email_id)}</span>
        <span className="min-w-0 truncate">
          <span className="font-medium">{e.template ?? "Inline content"}</span>
          <span className="text-ink-3"> to {e.to?.join(", ")}</span>
        </span>
        {e.error && (
          <Tag tone="danger" title={e.error}>
            {summarizeError(e.error)}
          </Tag>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-3">
          <span className="hidden md:inline-flex">
            <Label dot={e.tenant}>{e.tenant}</Label>
          </span>
          <span className="w-20 text-right text-xs text-ink-3">
            <Time iso={e.at} />
          </span>
        </span>
      </Link>
    </li>
  );
}

export function Activity() {
  const [params, setParams] = useSearchParams();
  const tenant = params.get("tenant") ?? "";
  const state = (params.get("state") ?? "") as State | "";
  // The state filter applies to the latest state of each email, so it runs
  // after grouping, not on the feed (where every email was "queued" once).
  const query = useActivity({ tenant }, true);
  const tenants = useTenants();

  function setFilter(key: "tenant" | "state", value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  }

  const emails = latestPerEmail(query.data?.pages.flatMap((p) => p.entries) ?? []).filter(
    (e) => !state || e.state === state,
  );
  const disabled = query.data?.pages[0]?.enabled === false;

  return (
    <Page
      title="Emails"
      description="Recent emails and their latest state. Recipients are masked."
      padded={false}
      actions={
        <>
          <label htmlFor="tenant" className="sr-only">
            Tenant
          </label>
          <Select id="tenant" size="sm" value={tenant} onChange={(e) => setFilter("tenant", e.target.value)}>
            <option value="">All tenants</option>
            {tenants.data?.map((t) => (
              <option key={t.id}>{t.id}</option>
            ))}
          </Select>
          <label htmlFor="state" className="sr-only">
            State
          </label>
          <Select id="state" size="sm" value={state} onChange={(e) => setFilter("state", e.target.value)}>
            <option value="">All states</option>
            {STATES.map((s) => (
              <option key={s} value={s}>
                {stateLabel(s)}
              </option>
            ))}
          </Select>
        </>
      }
    >
      {query.isPending && <ListSkeleton />}
      {query.error && (
        <div className="p-6">
          <ErrorNotice error={query.error} />
        </div>
      )}
      {disabled && (
        <EmptyState icon={EnvelopeSimple} title="The activity feed is disabled">
          Set KOURRIER_ACTIVITY_MAX_LEN above 0 to list recent emails.
        </EmptyState>
      )}
      {query.data && !disabled && emails.length === 0 && (
        <EmptyState icon={EnvelopeSimple} title="No email">
          No email matches these filters yet.
        </EmptyState>
      )}

      {GROUPS.map((g) => {
        const rows = emails.filter((e) => e.state === g);
        if (rows.length === 0) return null;
        return (
          <section key={g} aria-label={stateLabel(g)}>
            <h2 className="sticky top-0 z-10 flex h-9 items-center gap-2 border-b border-line bg-raised px-6 text-xs font-medium">
              <StatusIcon state={g} label={false} />
              {stateLabel(g)}
              <span className="tabular text-ink-3">{rows.length}</span>
            </h2>
            <ul>
              {rows.map((e) => (
                <Row key={e.email_id} e={e} />
              ))}
            </ul>
          </section>
        );
      })}

      {query.hasNextPage && (
        <div className="flex justify-center py-4">
          <Button size="sm" variant="ghost" onClick={() => void query.fetchNextPage()} busy={query.isFetchingNextPage}>
            Load older emails
          </Button>
        </div>
      )}
    </Page>
  );
}
