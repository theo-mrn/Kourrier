import { Eye, FileText } from "@phosphor-icons/react";
import { useState, type FormEvent } from "react";

import { apiURL } from "../api/client";
import { previewPath, previewQuery, usePreview, useTemplates, type PreviewRequest } from "../api/hooks";
import type { TemplateInfo } from "../api/types";
import { Page } from "../components/Layout";
import {
  Button,
  CodeBlock,
  EmptyState,
  ErrorNotice,
  Label,
  Select,
  Skeleton,
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

function Preview({ selected }: { selected: Selected }) {
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
    <div className="animate-rise p-6">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-xl font-medium tracking-tight">{template.name}</h2>
        <Label dot={tenant}>{tenant}</Label>
      </div>
      <p className="mt-1 text-sm text-ink-3">Render it with sample data. Nothing is sent.</p>

      <form onSubmit={render} className="mt-6 grid grid-cols-1 gap-4 md:grid-cols-[9rem_1fr_auto] md:items-start">
        <div>
          <label htmlFor="locale" className="mb-2 block text-xs text-ink-3">
            Locale
          </label>
          <Select id="locale" className="w-full" value={locale} onChange={(e) => setLocale(e.target.value)}>
            {template.locales.map((l) => (
              <option key={l}>{l}</option>
            ))}
          </Select>
        </div>
        <div>
          <label htmlFor="data" className="mb-2 block text-xs text-ink-3">
            Data, as a JSON object
          </label>
          <textarea
            id="data"
            className={cx(inputClass, "h-24 py-2 font-mono text-xs")}
            value={data}
            onChange={(e) => setData(e.target.value)}
            spellCheck={false}
            aria-invalid={invalid ? true : undefined}
            aria-describedby={invalid ? "data-error" : undefined}
          />
          {invalid && (
            <p id="data-error" className="mt-1 text-xs text-critical">
              {invalid}
            </p>
          )}
        </div>
        <Button
          type="submit"
          variant="primary"
          className="md:mt-6"
          disabled={Boolean(invalid)}
          busy={preview.isFetching}
        >
          <Eye aria-hidden className="size-4" />
          Render
        </Button>
      </form>

      {preview.error && (
        <div className="mt-6">
          <ErrorNotice error={preview.error} />
        </div>
      )}
      {preview.data && (
        <div className="mt-8">
          <p className="text-xs text-ink-3">Subject, locale {preview.data.locale}</p>
          <p className="mt-1 text-base font-medium">{preview.data.subject}</p>

          <div role="tablist" aria-label="Body" className="mt-4 flex gap-1">
            {(["html", "text"] as const).map((t) => (
              <button
                key={t}
                type="button"
                role="tab"
                aria-selected={tab === t}
                onClick={() => setTab(t)}
                className={cx(
                  "h-7 rounded-md px-3 text-xs transition-colors duration-200",
                  tab === t ? "bg-active text-ink" : "text-ink-3 hover:text-ink",
                )}
              >
                {t === "html" ? "HTML" : "Text"}
              </button>
            ))}
          </div>
          <div role="tabpanel" className="mt-3">
            {tab === "html" &&
              (iframeSrc ? (
                // Sandboxed, without scripts, under its own CSP (ADR 016).
                <iframe
                  title={`Rendered ${template.name}`}
                  sandbox=""
                  src={iframeSrc}
                  className="h-[34rem] w-full rounded-lg border border-line bg-white"
                />
              ) : (
                <p className="text-sm text-ink-3">This template has no HTML body.</p>
              ))}
            {tab === "text" &&
              (preview.data.text ? (
                <CodeBlock>{preview.data.text}</CodeBlock>
              ) : (
                <p className="text-sm text-ink-3">This template has no text body.</p>
              ))}
          </div>
        </div>
      )}
    </div>
  );
}

export function Templates() {
  const { data, error, isPending } = useTemplates();
  const [selected, setSelected] = useState<Selected>();

  return (
    <Page title="Templates" description="Read from the template storage of each tenant." padded={false}>
      {error && (
        <div className="p-6">
          <ErrorNotice error={error} />
        </div>
      )}
      <div className="flex min-h-full flex-col md:flex-row">
        <nav aria-label="Templates" className="shrink-0 border-b border-line py-3 md:w-72 md:border-r md:border-b-0">
          {isPending && (
            <div role="status" aria-label="Loading" className="flex flex-col gap-2 px-3">
              {Array.from({ length: 6 }, (_, i) => (
                <Skeleton key={i} className="h-8" />
              ))}
            </div>
          )}
          {data?.map((t) => (
            <div key={t.tenant} className="mb-4 px-3">
              <p className="mb-1 flex items-center gap-2 px-2 text-xs font-medium text-ink-3">{t.tenant}</p>
              {t.error && <p className="px-2 text-xs text-critical">{t.error}</p>}
              {!t.error && t.templates.length === 0 && <p className="px-2 text-xs text-ink-3">No template.</p>}
              <ul className="flex flex-col gap-0.5">
                {t.templates.map((tpl) => {
                  const active = selected?.tenant === t.tenant && selected.template.name === tpl.name;
                  return (
                    <li key={tpl.name}>
                      <button
                        type="button"
                        aria-current={active || undefined}
                        onClick={() => setSelected({ tenant: t.tenant, template: tpl })}
                        className={cx(
                          "flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm transition-colors duration-200",
                          active ? "bg-active text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
                        )}
                      >
                        <FileText aria-hidden className="size-4 shrink-0 opacity-70" />
                        <span className="flex-1 truncate">{tpl.name}</span>
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
            </div>
          ))}
        </nav>
        <div className="min-w-0 flex-1">
          {selected ? (
            <Preview key={`${selected.tenant}/${selected.template.name}`} selected={selected} />
          ) : (
            <EmptyState icon={FileText} title="Select a template">
              Check its subject, HTML and text with your own data before an application sends it.
            </EmptyState>
          )}
        </div>
      </div>
    </Page>
  );
}
