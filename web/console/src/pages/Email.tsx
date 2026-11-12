import { ArrowLeft } from "lucide-react";
import { Link, useParams } from "react-router";

import { ApiError } from "../api/client";
import { useEmail } from "../api/hooks";
import { Card, EmptyState, ErrorNotice, Loading, Mono, StatusStamp, Time } from "../components/ui";
import { dateTime } from "../lib/format";

export function Email() {
  const { id = "" } = useParams();
  const { data, error, isPending } = useEmail(id);
  const notFound = error instanceof ApiError && error.status === 404;

  return (
    <>
      <Link
        to="/activity"
        className="mb-4 inline-flex items-center gap-1.5 text-sm font-semibold text-blue hover:underline"
      >
        <ArrowLeft aria-hidden className="size-4" /> Activity
      </Link>
      <h1 className="display text-3xl font-bold">E-mail</h1>
      <p className="mt-1 mb-6 break-all">
        <Mono className="text-ink-2">{id}</Mono>
      </p>

      {isPending && <Loading />}
      {notFound && (
        <EmptyState title="No such e-mail">
          Either the ID is wrong, or its status expired (<code className="font-mono">KOURRIER_STATUS_TTL</code>) and it
          left the activity feed.
        </EmptyState>
      )}
      {error && !notFound && <ErrorNotice error={error} />}

      {data && (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[22rem_1fr]">
          <Card className="h-fit p-5">
            {data.status ? (
              <>
                <StatusStamp state={data.status.status} large />
                <dl className="mt-5 grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                  <dt className="text-ink-2">Tenant</dt>
                  <dd>{data.tenant}</dd>
                  <dt className="text-ink-2">Attempts</dt>
                  <dd className="tabular">{data.status.attempts}</dd>
                  {data.status.provider && (
                    <>
                      <dt className="text-ink-2">Provider</dt>
                      <dd>{data.status.provider}</dd>
                    </>
                  )}
                  {data.status.provider_message_id && (
                    <>
                      <dt className="text-ink-2">Message ID</dt>
                      <dd className="break-all">
                        <Mono>{data.status.provider_message_id}</Mono>
                      </dd>
                    </>
                  )}
                  {data.status.next_attempt_at && (
                    <>
                      <dt className="text-ink-2">Next attempt</dt>
                      <dd>{dateTime(data.status.next_attempt_at)}</dd>
                    </>
                  )}
                  <dt className="text-ink-2">Created</dt>
                  <dd>{dateTime(data.status.created_at)}</dd>
                  <dt className="text-ink-2">Updated</dt>
                  <dd>{dateTime(data.status.updated_at)}</dd>
                </dl>
                {data.status.last_error && (
                  <p className="mt-4 rounded-m border border-failed/40 bg-failed-soft/40 p-3 font-mono text-[13px] break-words text-failed">
                    {data.status.last_error}
                  </p>
                )}
              </>
            ) : (
              <p className="text-sm text-ink-2">The status expired; the activity feed still remembers this e-mail.</p>
            )}
          </Card>

          <section aria-labelledby="timeline-title">
            <h2 id="timeline-title" className="display mb-3 text-xl font-semibold">
              Timeline
            </h2>
            {data.activity.length === 0 ? (
              <p className="text-sm text-ink-2">No entry in the activity feed.</p>
            ) : (
              <ol className="relative border-l-2 border-line pl-6">
                {data.activity.map((e) => (
                  <li key={e.id} className="relative pb-6 last:pb-0">
                    <span
                      aria-hidden
                      className="absolute top-1.5 -left-[1.95rem] size-3 rounded-full border-2 border-card bg-ink-3"
                    />
                    <div className="flex flex-wrap items-center gap-3">
                      <StatusStamp state={e.state} />
                      <span className="text-sm text-ink-2">
                        <Time iso={e.at} />
                        {e.attempt ? ` · attempt ${e.attempt}` : ""}
                        {e.origin ? ` · via ${e.origin}` : ""}
                      </span>
                    </div>
                    {(e.template || e.to) && (
                      <p className="mt-1.5 text-sm">
                        {e.template && <span>Template {e.template}</span>}
                        {e.to && <Mono className="ml-2 text-ink-2">→ {e.to.join(", ")}</Mono>}
                      </p>
                    )}
                    {e.error && <p className="mt-1.5 font-mono text-[13px] break-words text-failed">{e.error}</p>}
                  </li>
                ))}
              </ol>
            )}
          </section>
        </div>
      )}
    </>
  );
}
