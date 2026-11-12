import { ArrowRight } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";

import { useOverview } from "../api/hooks";
import { Card, ErrorNotice, Loading, PageHeader, Time, cx } from "../components/ui";
import { count } from "../lib/format";

function Stat({
  label,
  value,
  hint,
  ink = "text-ink",
  to,
}: {
  label: string;
  value: number;
  hint: string;
  ink?: string;
  to?: string;
}) {
  const body = (
    <>
      <p className="font-mono text-[11px] tracking-wide text-ink-3 uppercase">{label}</p>
      <p className={cx("display tabular mt-2 text-4xl font-bold", ink)}>{count(value)}</p>
      <p className="mt-1 text-sm text-ink-2">{hint}</p>
    </>
  );
  return (
    <Card className="p-5">
      {to ? (
        <Link to={to} className="group block">
          {body}
          <span className="mt-3 inline-flex items-center gap-1 text-sm font-semibold text-blue group-hover:underline">
            Open <ArrowRight aria-hidden className="size-3.5" />
          </span>
        </Link>
      ) : (
        body
      )}
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-line py-2.5 last:border-b-0">
      <dt className="text-ink-2">{label}</dt>
      <dd className="text-right font-mono text-[13px]">{children}</dd>
    </div>
  );
}

export function Overview() {
  const { data, error, isPending } = useOverview();

  return (
    <>
      <PageHeader title="Overview" description="The queue right now, refreshed every five seconds." />
      {isPending && <Loading />}
      {error && <ErrorNotice error={error} />}
      {data && (
        <div className="flex flex-col gap-8">
          <section aria-labelledby="queue-title">
            <h2 id="queue-title" className="sr-only">
              Queue
            </h2>
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
              <Stat label="Ready" value={data.queue.ready} hint="waiting for a worker" ink="text-queued" />
              <Stat label="In progress" value={data.queue.pending} hint="held by a worker" />
              <Stat
                label="Retrying"
                value={data.queue.delayed}
                hint="waiting for their next attempt"
                ink="text-retrying"
              />
              <Stat
                label="Dead letters"
                value={data.queue.dead}
                hint="failed for good"
                ink={data.queue.dead > 0 ? "text-failed" : "text-ink"}
                to="/dead-letters"
              />
            </div>
          </section>

          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <Card className="p-5">
              <h2 className="display text-xl font-semibold">Instance</h2>
              <dl className="mt-3 text-sm">
                <Row label="Version">{data.version}</Row>
                <Row label="Mode">{data.mode}</Row>
                <Row label="Mailer">{data.mailer}</Row>
                <Row label="Template storage">{data.storage}</Row>
                <Row label="Tenants">{count(data.tenants)}</Row>
                <Row label="Activity feed">{data.activity ? "on" : "off"}</Row>
                <Row label="Started">
                  <Time iso={data.started_at} />
                </Row>
              </dl>
            </Card>

            <Card className="p-5">
              <h2 className="display text-xl font-semibold">Event ingress</h2>
              {data.events ? (
                <>
                  <dl className="mt-3 text-sm">
                    <Row label="Routes">{count(data.events.routes)}</Row>
                    <Row label="NATS sources">{count(data.events.sources)}</Row>
                    <Row label="Failed events">
                      <span className={data.events.dead > 0 ? "text-failed" : undefined}>
                        {count(data.events.dead)}
                      </span>
                    </Row>
                  </dl>
                  <div className="mt-4 flex gap-4 text-sm font-semibold">
                    <Link to="/failed-events" className="text-blue hover:underline">
                      Failed events
                    </Link>
                    <Link to="/routes" className="text-blue hover:underline">
                      Routes
                    </Link>
                  </div>
                </>
              ) : (
                <p className="mt-3 text-sm text-ink-2">
                  Disabled: no routes file is configured (
                  <code className="font-mono text-[13px]">KOURRIER_EVENTS_ROUTES_FILE</code>).
                </p>
              )}
            </Card>
          </div>
        </div>
      )}
    </>
  );
}
