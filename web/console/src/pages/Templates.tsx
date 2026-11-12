import { Eye } from "lucide-react";
import { useState, type FormEvent } from "react";

import { apiURL } from "../api/client";
import { previewPath, previewQuery, usePreview, useTemplates, type PreviewRequest } from "../api/hooks";
import type { TemplateInfo } from "../api/types";
import {
  Button,
  Card,
  CodeBlock,
  EmptyState,
  ErrorNotice,
  Field,
  Loading,
  PageHeader,
  Tag,
  cx,
  inputClass,
} from "../components/ui";

interface Selected {
  tenant: string;
  template: TemplateInfo;
}

function parseData(raw: string): string | null {
  if (!raw.trim()) return null;
  try {
    const v: unknown = JSON.parse(raw);
    return v !== null && typeof v === "object" && !Array.isArray(v) ? null : "Data must be a JSON object.";
  } catch {
    return "Data is not valid JSON.";
  }
}

function PreviewPanel({ selected }: { selected: Selected }) {
  const { tenant, template } = selected;
  const [locale, setLocale] = useState(template.locales[0] ?? "");
  const [data, setData] = useState("{}");
  const [request, setRequest] = useState<PreviewRequest | null>(null);
  const [tab, setTab] = useState<"html" | "text">("html");
  const invalid = parseData(data);
  const preview = usePreview(request);

  function render(e: FormEvent) {
    e.preventDefault();
    if (!invalid) setRequest({ tenant, name: template.name, locale, data });
  }

  const html = preview.data?.html;
  const iframeSrc = request && html ? apiURL(previewPath(request, true), previewQuery(request)) : null;

  return (
    <Card className="p-5">
      <h2 className="display text-2xl font-semibold">{template.name}</h2>
      <p className="text-sm text-ink-2">Tenant {tenant}. Nothing is sent: the template is only rendered.</p>

      <form onSubmit={render} className="mt-5 grid grid-cols-1 gap-4 sm:grid-cols-[10rem_1fr]">
        <Field label="Locale">
          <select className={inputClass} value={locale} onChange={(e) => setLocale(e.target.value)}>
            {template.locales.map((l) => (
              <option key={l}>{l}</option>
            ))}
          </select>
        </Field>
        <Field label="Data (JSON)">
          <textarea
            className={cx(inputClass, "min-h-28 font-mono text-[13px]")}
            value={data}
            onChange={(e) => setData(e.target.value)}
            spellCheck={false}
            aria-invalid={invalid ? true : undefined}
            aria-describedby={invalid ? "data-error" : undefined}
          />
          {invalid && (
            <span id="data-error" className="text-failed">
              {invalid}
            </span>
          )}
        </Field>
        <div className="sm:col-start-2">
          <Button type="submit" variant="primary" disabled={Boolean(invalid)} busy={preview.isFetching}>
            <Eye aria-hidden className="size-4" />
            Render
          </Button>
        </div>
      </form>

      {preview.error && (
        <div className="mt-5">
          <ErrorNotice error={preview.error} />
        </div>
      )}
      {preview.data && (
        <div className="mt-6">
          <p className="font-mono text-[11px] tracking-wide text-ink-3 uppercase">
            Subject · locale {preview.data.locale}
          </p>
          <p className="display mt-1 text-xl font-semibold">{preview.data.subject}</p>

          <div role="tablist" aria-label="Body" className="mt-4 flex gap-1 border-b border-line">
            {(["html", "text"] as const).map((t) => (
              <button
                key={t}
                type="button"
                role="tab"
                aria-selected={tab === t}
                onClick={() => setTab(t)}
                className={cx(
                  "-mb-px border-b-2 px-3 py-2 text-sm font-semibold",
                  tab === t ? "border-ink text-ink" : "border-transparent text-ink-3 hover:text-ink",
                )}
              >
                {t === "html" ? "HTML" : "Text"}
              </button>
            ))}
          </div>
          <div role="tabpanel" className="mt-4">
            {tab === "html" &&
              (iframeSrc ? (
                // Sandboxed, without scripts, under its own CSP (ADR 016).
                <iframe
                  title={`Preview of ${template.name}`}
                  sandbox=""
                  src={iframeSrc}
                  className="h-[32rem] w-full rounded-m border border-line bg-white"
                />
              ) : (
                <p className="text-sm text-ink-2">This template has no HTML body.</p>
              ))}
            {tab === "text" &&
              (preview.data.text ? (
                <CodeBlock>{preview.data.text}</CodeBlock>
              ) : (
                <p className="text-sm text-ink-2">This template has no text body.</p>
              ))}
          </div>
        </div>
      )}
    </Card>
  );
}

export function Templates() {
  const { data, error, isPending } = useTemplates();
  const [selected, setSelected] = useState<Selected>();

  return (
    <>
      <PageHeader
        title="Templates"
        description="The templates of each tenant, read from the template storage, and a preview with your own data."
      />
      {isPending && <Loading />}
      {error && <ErrorNotice error={error} />}
      {data && (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[18rem_1fr]">
          <nav aria-label="Templates" className="flex flex-col gap-5">
            {data.map((t) => (
              <section key={t.tenant}>
                <h2 className="stamp-type mb-2 text-xs text-ink-3">{t.tenant}</h2>
                {t.error && <p className="text-sm text-failed">{t.error}</p>}
                {!t.error && t.templates.length === 0 && <p className="text-sm text-ink-3">No template.</p>}
                <ul className="flex flex-col gap-1">
                  {t.templates.map((tpl) => {
                    const active = selected?.tenant === t.tenant && selected.template.name === tpl.name;
                    return (
                      <li key={tpl.name}>
                        <button
                          type="button"
                          aria-current={active || undefined}
                          onClick={() => setSelected({ tenant: t.tenant, template: tpl })}
                          className={cx(
                            "flex w-full items-center justify-between gap-2 rounded-m border px-3 py-2 text-left text-sm",
                            active ? "border-ink bg-card" : "border-transparent hover:border-line hover:bg-card",
                          )}
                        >
                          <span className="font-medium">{tpl.name}</span>
                          <span className="flex gap-1">
                            {tpl.locales.map((l) => (
                              <Tag key={l}>{l}</Tag>
                            ))}
                          </span>
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </section>
            ))}
          </nav>
          {selected ? (
            <PreviewPanel key={`${selected.tenant}/${selected.template.name}`} selected={selected} />
          ) : (
            <EmptyState title="Pick a template">
              Choose a template on the left to render it with sample data.
            </EmptyState>
          )}
        </div>
      )}
    </>
  );
}
