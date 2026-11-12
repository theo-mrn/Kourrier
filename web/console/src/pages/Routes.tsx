import { ArrowRight, Path, Plug } from "@phosphor-icons/react";

import { useRoutes } from "../api/hooks";
import { Page } from "../components/Layout";
import { EmptyState, ErrorNotice, Label, ListSkeleton, Section, Tag } from "../components/ui";

export function RoutesPage() {
  const { data, error, isPending } = useRoutes();

  return (
    <Page title="Routes" description="How CloudEvents become emails, as loaded at startup.">
      {isPending && <ListSkeleton rows={3} />}
      {error && <ErrorNotice error={error} />}
      {data && !data.enabled && (
        <EmptyState icon={Path} title="The event ingress is disabled">
          Set KOURRIER_EVENTS_ROUTES_FILE to consume CloudEvents.
        </EmptyState>
      )}
      {data?.enabled && (
        <div className="flex flex-col gap-4">
          <Section title={`${data.routes.length} ${data.routes.length === 1 ? "route" : "routes"}`}>
            <ul>
              {data.routes.map((r) => (
                <li key={`${r.tenant}/${r.name}`} className="border-b border-line px-4 py-3 last:border-b-0">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <span className="font-medium">{r.name}</span>
                    <Label dot={r.tenant}>{r.tenant}</Label>
                  </div>
                  <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-ink-2">
                    {r.types.map((t) => (
                      <Tag key={t}>
                        <span className="font-mono">{t}</span>
                      </Tag>
                    ))}
                    {r.when && (
                      <>
                        <span className="text-ink-3">when</span>
                        <code className="rounded bg-app px-2 py-0.5 font-mono text-ink">{r.when}</code>
                      </>
                    )}
                    <ArrowRight aria-hidden className="size-3 text-ink-3" />
                    <span className="font-medium text-ink">{r.template}</span>
                  </div>
                </li>
              ))}
            </ul>
          </Section>

          <Section title="NATS sources">
            {data.sources.length === 0 ? (
              <p className="p-4 text-sm text-ink-3">No NATS source. Events only arrive through POST /v1/events.</p>
            ) : (
              <ul>
                {data.sources.map((s) => (
                  <li key={s.name} className="flex items-start gap-3 border-b border-line px-4 py-3 last:border-b-0">
                    <Plug aria-hidden className="mt-0.5 size-4 text-ink-3" />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2 text-sm">
                        <span className="font-medium">{s.name}</span>
                        <Label dot={s.tenant}>{s.tenant}</Label>
                        {s.on_unrouted === "dead_letter" && <Tag>Keeps unrouted events</Tag>}
                        {s.bind && <Tag>Existing consumer</Tag>}
                      </div>
                      <p className="mt-1 font-mono text-xs text-ink-3">
                        {s.stream} / {s.consumer}
                        {s.filter_subjects?.length ? ` · ${s.filter_subjects.join(", ")}` : ""}
                      </p>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </Section>
        </div>
      )}
    </Page>
  );
}
