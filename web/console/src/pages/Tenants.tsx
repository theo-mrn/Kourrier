import { useTenants } from "../api/hooks";
import { Card, EmptyState, ErrorNotice, Loading, Mono, PageHeader, Tag } from "../components/ui";
import { plural } from "../lib/format";

export function Tenants() {
  const { data, error, isPending } = useTenants();

  return (
    <>
      <PageHeader
        title="Tenants"
        description={
          <>
            The applications allowed to use Kourrier, from the tenants file. Only SHA-256 digests of the keys are
            stored, and the console never shows them.
          </>
        }
      />
      {isPending && <Loading />}
      {error && <ErrorNotice error={error} />}
      {data?.length === 0 && <EmptyState title="No tenant" />}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        {data?.map((t) => (
          <Card key={t.id} className="p-5">
            <h2 className="display text-2xl font-semibold">{t.id}</h2>
            <dl className="mt-4 flex flex-col gap-4 text-sm">
              <div>
                <dt className="font-mono text-[11px] tracking-wide text-ink-3 uppercase">Default sender</dt>
                <dd className="mt-1">
                  {t.default_sender.email ? (
                    <>
                      {t.default_sender.name && <span className="mr-1">{t.default_sender.name}</span>}
                      <Mono className="text-ink-2">&lt;{t.default_sender.email}&gt;</Mono>
                    </>
                  ) : (
                    <span className="text-ink-3">none: requests must set from</span>
                  )}
                </dd>
              </div>
              <div>
                <dt className="font-mono text-[11px] tracking-wide text-ink-3 uppercase">Allowed senders</dt>
                <dd className="mt-1 flex flex-wrap gap-1.5">
                  {t.allowed_senders.map((s) => (
                    <Mono key={s} className="rounded-s bg-sunken px-1.5 py-0.5">
                      {s}
                    </Mono>
                  ))}
                </dd>
              </div>
              <div>
                <dt className="font-mono text-[11px] tracking-wide text-ink-3 uppercase">
                  {plural(t.api_keys.length, "API key")}
                </dt>
                <dd className="mt-1">
                  <ul className="flex flex-col gap-1.5">
                    {t.api_keys.map((k, i) => (
                      <li key={i} className="flex items-center gap-2">
                        <span className="text-ink-3">Key {i + 1}</span>
                        {k.scopes.map((s) => (
                          <Tag key={s} tone={s === "emails" ? "blue" : "neutral"}>
                            {s}
                          </Tag>
                        ))}
                      </li>
                    ))}
                  </ul>
                </dd>
              </div>
            </dl>
          </Card>
        ))}
      </div>
    </>
  );
}
