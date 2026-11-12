import { CaretRight, EnvelopeSimple } from "@phosphor-icons/react";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router";

import { ApiError } from "../api/client";
import { useEmail } from "../api/hooks";
import { Page } from "../components/Layout";
import { EmptyState, ErrorNotice, Label, Skeleton, StatusIcon, StatusValue, Time, stateLabel } from "../components/ui";
import { summarizeError } from "../lib/errors";
import { dateTime, shortID } from "../lib/format";

function Property({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-h-8 items-start gap-3 py-1 text-sm">
      <dt className="w-24 shrink-0 pt-0.5 text-xs text-ink-3">{label}</dt>
      <dd className="min-w-0 flex-1 break-words">{children}</dd>
    </div>
  );
}

export function Email() {
  const { id = "" } = useParams();
  const { data, error, isPending } = useEmail(id);
  const notFound = error instanceof ApiError && error.status === 404;
  const latest = data?.activity[0];
  const status = data?.status;

  return (
    <Page
      padded={false}
      title={
        <span className="flex items-center gap-1 text-ink-3">
          <Link to="/activity" className="hover:text-ink">
            Emails
          </Link>
          <CaretRight aria-hidden className="size-3" />
          <span className="font-mono text-xs text-ink">{shortID(id)}</span>
        </span>
      }
    >
      {isPending && (
        <div role="status" aria-label="Loading" className="p-8">
          <Skeleton className="h-7 w-72" />
          <Skeleton className="mt-3 h-4 w-48" />
        </div>
      )}
      {notFound && (
        <EmptyState icon={EnvelopeSimple} title="No such email">
          The ID is wrong, or its status expired (KOURRIER_STATUS_TTL) and it left the activity feed.
        </EmptyState>
      )}
      {error && !notFound && (
        <div className="p-6">
          <ErrorNotice error={error} />
        </div>
      )}

      {data && (
        <div className="flex min-h-full flex-col lg:flex-row">
          <div className="min-w-0 flex-1 px-8 py-8">
            <h2 className="text-2xl font-medium tracking-tight">{latest?.template ?? "Email"}</h2>
            {latest?.to && <p className="mt-1 text-ink-3">To {latest.to.join(", ")}</p>}

            {status?.last_error && (
              <div className="mt-6 rounded-lg border border-critical/30 bg-critical/8 p-4">
                <p className="flex items-center gap-2 text-sm font-medium text-critical">
                  <StatusIcon state="failed" label={false} />
                  {summarizeError(status.last_error)}
                </p>
                <pre className="mt-2 font-mono text-xs leading-5 break-words whitespace-pre-wrap text-ink-2">
                  {status.last_error}
                </pre>
              </div>
            )}

            <h3 className="mt-10 text-sm font-medium">Activity</h3>
            {data.activity.length === 0 ? (
              <p className="mt-3 text-sm text-ink-3">No entry in the activity feed.</p>
            ) : (
              <ol className="mt-4 flex flex-col">
                {data.activity.map((e, i) => (
                  <li key={e.id} className="relative flex gap-3 pb-5 last:pb-0">
                    {i < data.activity.length - 1 && (
                      <span aria-hidden className="absolute top-5 bottom-0 left-[6px] w-px bg-line-strong" />
                    )}
                    <span className="mt-0.5">
                      <StatusIcon state={e.state} label={false} />
                    </span>
                    <p className="text-sm">
                      <span className="font-medium">{stateLabel(e.state)}</span>
                      <span className="text-ink-3">
                        {e.origin ? ` from ${e.origin === "api" ? "the API" : "an event"}` : ""}
                        {e.attempt ? `, attempt ${e.attempt}` : ""} · <Time iso={e.at} />
                      </span>
                      {e.error && <span className="mt-1 block text-critical">{summarizeError(e.error)}</span>}
                    </p>
                  </li>
                ))}
              </ol>
            )}
          </div>

          <aside aria-label="Properties" className="border-t border-line px-6 py-6 lg:w-80 lg:border-t-0 lg:border-l">
            <dl>
              {status ? (
                <Property label="Status">
                  <StatusValue state={status.status} />
                </Property>
              ) : (
                <Property label="Status">
                  <span className="text-ink-3">Expired</span>
                </Property>
              )}
              {data.tenant && (
                <Property label="Tenant">
                  <Label dot={data.tenant}>{data.tenant}</Label>
                </Property>
              )}
              {latest?.template && <Property label="Template">{latest.template}</Property>}
              {status && <Property label="Attempts">{status.attempts}</Property>}
              {status?.provider && <Property label="Provider">{status.provider}</Property>}
              {status?.next_attempt_at && <Property label="Next attempt">{dateTime(status.next_attempt_at)}</Property>}
              {status && <Property label="Created">{dateTime(status.created_at)}</Property>}
              {status && <Property label="Updated">{dateTime(status.updated_at)}</Property>}
              <Property label="ID">
                <span className="font-mono text-xs text-ink-2">{id}</span>
              </Property>
              {status?.provider_message_id && (
                <Property label="Message ID">
                  <span className="font-mono text-xs text-ink-2">{status.provider_message_id}</span>
                </Property>
              )}
            </dl>
          </aside>
        </div>
      )}
    </Page>
  );
}
