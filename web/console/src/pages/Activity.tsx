import { Pause, Play, Search } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";

import { useActivity, useTenants, type ActivityFilters } from "../api/hooks";
import { STATES, type State } from "../api/types";
import { shortID } from "../lib/format";
import {
  Button,
  EmptyState,
  ErrorNotice,
  Field,
  Loading,
  Mono,
  PageHeader,
  StatusStamp,
  Time,
  inputClass,
  table,
} from "../components/ui";

export function Activity() {
  const [params, setParams] = useSearchParams();
  const filters: ActivityFilters = {
    tenant: params.get("tenant") ?? "",
    state: (params.get("state") ?? "") as State | "",
  };
  const [live, setLive] = useState(true);
  const query = useActivity(filters, live);
  const tenants = useTenants();
  const navigate = useNavigate();
  const [lookup, setLookup] = useState("");

  function setFilter(key: "tenant" | "state", value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  }

  function findEmail(e: FormEvent) {
    e.preventDefault();
    if (lookup.trim()) void navigate(`/emails/${encodeURIComponent(lookup.trim())}`);
  }

  const entries = query.data?.pages.flatMap((p) => p.entries) ?? [];
  const disabled = query.data?.pages[0]?.enabled === false;

  return (
    <>
      <PageHeader
        title="Activity"
        description="The latest status changes of every e-mail, newest first. Recipients are masked."
        actions={
          <Button onClick={() => setLive(!live)} aria-pressed={live}>
            {live ? <Pause aria-hidden className="size-4" /> : <Play aria-hidden className="size-4" />}
            {live ? "Live" : "Paused"}
          </Button>
        }
      />

      <div className="mb-5 flex flex-col gap-3 lg:flex-row lg:items-end">
        <Field label="Tenant" className="lg:w-48">
          <select className={inputClass} value={filters.tenant} onChange={(e) => setFilter("tenant", e.target.value)}>
            <option value="">All tenants</option>
            {tenants.data?.map((t) => (
              <option key={t.id}>{t.id}</option>
            ))}
          </select>
        </Field>
        <Field label="State" className="lg:w-40">
          <select className={inputClass} value={filters.state} onChange={(e) => setFilter("state", e.target.value)}>
            <option value="">All states</option>
            {STATES.map((s) => (
              <option key={s}>{s}</option>
            ))}
          </select>
        </Field>
        <form onSubmit={findEmail} className="flex flex-1 items-end gap-2 lg:justify-end" role="search">
          <Field label="Find an e-mail by ID" className="flex-1 lg:max-w-sm">
            <input
              className={inputClass}
              value={lookup}
              onChange={(e) => setLookup(e.target.value)}
              placeholder="01a1d128-48c7-7ab5-8d6f-…"
              spellCheck={false}
            />
          </Field>
          <Button type="submit" aria-label="Find the e-mail">
            <Search aria-hidden className="size-4" />
          </Button>
        </form>
      </div>

      {query.isPending && <Loading />}
      {query.error && <ErrorNotice error={query.error} />}
      {disabled && (
        <EmptyState title="The activity feed is disabled">
          Set <code className="font-mono">KOURRIER_ACTIVITY_MAX_LEN</code> above 0 to record status changes.
        </EmptyState>
      )}
      {query.data && !disabled && entries.length === 0 && (
        <EmptyState title="Nothing yet">No status change matches these filters.</EmptyState>
      )}

      {entries.length > 0 && (
        <div className={table.wrap}>
          <table className={table.table}>
            <thead>
              <tr>
                <th scope="col" className={table.th}>
                  When
                </th>
                <th scope="col" className={table.th}>
                  State
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
                  Detail
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={e.id} className="hover:bg-page/60">
                  <td className={table.td}>
                    <Time iso={e.at} />
                  </td>
                  <td className={table.td}>
                    <StatusStamp state={e.state} />
                  </td>
                  <td className={table.td}>
                    <Link to={`/emails/${e.email_id}`} className="text-blue hover:underline">
                      <Mono>{shortID(e.email_id)}</Mono>
                    </Link>
                  </td>
                  <td className={table.td}>{e.tenant}</td>
                  <td className={table.td}>{e.template ?? <span className="text-ink-3">inline</span>}</td>
                  <td className={table.td}>
                    <Mono className="text-ink-2">{e.to?.join(", ")}</Mono>
                  </td>
                  <td className={`${table.td} max-w-xs text-ink-2`}>
                    {e.error ? (
                      <span className="line-clamp-2 text-failed" title={e.error}>
                        {e.error}
                      </span>
                    ) : e.origin ? (
                      `via ${e.origin}`
                    ) : e.attempt ? (
                      `attempt ${e.attempt}`
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {query.hasNextPage && (
        <div className="mt-4 flex justify-center">
          <Button onClick={() => void query.fetchNextPage()} busy={query.isFetchingNextPage}>
            Load older entries
          </Button>
        </div>
      )}
    </>
  );
}
