import { Buildings, Key } from "@phosphor-icons/react";

import { useTenants } from "../api/hooks";
import { Page } from "../components/Layout";
import { EmptyState, ErrorNotice, Label, Skeleton, Tag } from "../components/ui";
import { plural } from "../lib/format";

export function Tenants() {
  const { data, error, isPending } = useTenants();

  return (
    <Page title="Tenants" description="The applications allowed to send. Key digests are never shown.">
      {isPending && (
        <div role="status" aria-label="Loading" className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <Skeleton className="h-48 rounded-lg" />
          <Skeleton className="h-48 rounded-lg" />
        </div>
      )}
      {error && <ErrorNotice error={error} />}
      {data?.length === 0 && <EmptyState icon={Buildings} title="No tenant" />}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {data?.map((t) => (
          <section key={t.id} className="surface-card rounded-xl border border-line-strong">
            <header className="flex h-12 items-center gap-3 border-b border-line px-4">
              <Label dot={t.id}>{t.id}</Label>
              <span className="ml-auto text-xs text-ink-3">{plural(t.api_keys.length, "API key")}</span>
            </header>
            <dl className="flex flex-col gap-4 p-4 text-sm">
              <div>
                <dt className="text-xs text-ink-3">Default sender</dt>
                <dd className="mt-1">
                  {t.default_sender.email ? (
                    <>
                      {t.default_sender.name && `${t.default_sender.name} `}
                      <span className="text-ink-3">&lt;{t.default_sender.email}&gt;</span>
                    </>
                  ) : (
                    <span className="text-ink-3">None. Requests must set a sender.</span>
                  )}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-ink-3">Allowed senders</dt>
                <dd className="mt-2 flex flex-wrap gap-2">
                  {t.allowed_senders.map((s) => (
                    <Tag key={s}>
                      <span className="font-mono">{s}</span>
                    </Tag>
                  ))}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-ink-3">Keys and scopes</dt>
                <dd className="mt-2 flex flex-col gap-2">
                  {t.api_keys.map((k, i) => (
                    <p key={i} className="flex items-center gap-2">
                      <Key aria-hidden className="size-4 text-ink-3" />
                      <span className="text-ink-2">Key {i + 1}</span>
                      {k.scopes.map((s) => (
                        <Tag key={s}>{s}</Tag>
                      ))}
                    </p>
                  ))}
                </dd>
              </div>
            </dl>
          </section>
        ))}
      </div>
    </Page>
  );
}
