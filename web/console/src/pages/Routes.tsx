import { useRoutes } from "../api/hooks";
import { EmptyState, ErrorNotice, Loading, Mono, PageHeader, Tag, table } from "../components/ui";

export function RoutesPage() {
  const { data, error, isPending } = useRoutes();

  return (
    <>
      <PageHeader
        title="Routes"
        description="How CloudEvents become e-mails: the routes and NATS sources of the routes file, as loaded at startup."
      />
      {isPending && <Loading />}
      {error && <ErrorNotice error={error} />}
      {data && !data.enabled && (
        <EmptyState title="The event ingress is disabled">
          Configure a routes file (<code className="font-mono">KOURRIER_EVENTS_ROUTES_FILE</code>) to consume
          CloudEvents.
        </EmptyState>
      )}
      {data?.enabled && (
        <div className="flex flex-col gap-8">
          <section aria-labelledby="routes-title">
            <h2 id="routes-title" className="display mb-3 text-xl font-semibold">
              Routes
            </h2>
            {data.routes.length === 0 ? (
              <EmptyState title="No route" />
            ) : (
              <div className={table.wrap}>
                <table className={table.table}>
                  <thead>
                    <tr>
                      <th scope="col" className={table.th}>
                        Route
                      </th>
                      <th scope="col" className={table.th}>
                        Tenant
                      </th>
                      <th scope="col" className={table.th}>
                        Event types
                      </th>
                      <th scope="col" className={table.th}>
                        Condition (CEL)
                      </th>
                      <th scope="col" className={table.th}>
                        Template
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.routes.map((r) => (
                      <tr key={`${r.tenant}/${r.name}`}>
                        <td className={`${table.td} font-medium`}>{r.name}</td>
                        <td className={table.td}>{r.tenant}</td>
                        <td className={table.td}>
                          <ul>
                            {r.types.map((t) => (
                              <li key={t}>
                                <Mono>{t}</Mono>
                              </li>
                            ))}
                          </ul>
                          {r.source && <p className="mt-1 text-xs text-ink-3">source {r.source}</p>}
                        </td>
                        <td className={table.td}>
                          {r.when ? (
                            <code className="rounded-s bg-code px-1.5 py-0.5 font-mono text-[13px] text-code-text">
                              {r.when}
                            </code>
                          ) : (
                            <span className="text-ink-3">always</span>
                          )}
                        </td>
                        <td className={table.td}>{r.template}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>

          <section aria-labelledby="sources-title">
            <h2 id="sources-title" className="display mb-3 text-xl font-semibold">
              NATS sources
            </h2>
            {data.sources.length === 0 ? (
              <p className="text-sm text-ink-2">No NATS source: events only arrive through POST /v1/events.</p>
            ) : (
              <div className={table.wrap}>
                <table className={table.table}>
                  <thead>
                    <tr>
                      <th scope="col" className={table.th}>
                        Source
                      </th>
                      <th scope="col" className={table.th}>
                        Tenant
                      </th>
                      <th scope="col" className={table.th}>
                        Stream / consumer
                      </th>
                      <th scope="col" className={table.th}>
                        Subjects
                      </th>
                      <th scope="col" className={table.th}>
                        Options
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.sources.map((s) => (
                      <tr key={s.name}>
                        <td className={`${table.td} font-medium`}>{s.name}</td>
                        <td className={table.td}>{s.tenant}</td>
                        <td className={table.td}>
                          <Mono>
                            {s.stream} / {s.consumer}
                          </Mono>
                        </td>
                        <td className={table.td}>
                          <Mono className="text-ink-2">{s.filter_subjects?.join(", ") || "all"}</Mono>
                        </td>
                        <td className={table.td}>
                          <span className="flex flex-wrap gap-1">
                            {s.bind && <Tag>bind</Tag>}
                            {s.on_unrouted && <Tag>unrouted: {s.on_unrouted}</Tag>}
                            {s.deliver_policy && <Tag>deliver: {s.deliver_policy}</Tag>}
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
        </div>
      )}
    </>
  );
}
