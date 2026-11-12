import { ArrowRight, EnvelopeSimple, MagnifyingGlass } from "@phosphor-icons/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";

import { navGroups } from "./nav";
import { Kbd, cx } from "./ui";

interface Command {
  id: string;
  label: string;
  hint?: string;
  run: () => void;
  icon: typeof EnvelopeSimple;
}

// UUIDv7 e-mail IDs: offer to open one when it is typed or pasted.
const ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** ⌘K: jump to any page, or to an email by its ID. */
export function CommandMenu({ open, onClose }: { open: boolean; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal?.();
    if (!open && dialog.open) dialog.close?.();
  }, [open]);

  const commands = useMemo<Command[]>(() => {
    const go = (to: string) => () => {
      void navigate(to);
      onClose();
    };
    const q = query.trim();
    const pages = navGroups()
      .flatMap((g) => g.items.map((i) => ({ ...i, group: g.label ?? "Monitor" })))
      .filter((i) => i.label.toLowerCase().includes(q.toLowerCase()))
      .map((i) => ({ id: i.to, label: i.label, hint: i.group, icon: i.icon, run: go(i.to) }));
    if (ID.test(q)) {
      return [{ id: "email", label: `Open email ${q}`, icon: EnvelopeSimple, run: go(`/emails/${q}`) }, ...pages];
    }
    return pages;
  }, [query, navigate, onClose]);

  function close() {
    setQuery("");
    setActive(0);
    onClose();
  }

  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
      onClick={(e) => e.target === ref.current && close()}
      aria-label="Command menu"
      className="animate-pop mx-auto mt-[15vh] w-[min(36rem,calc(100vw-2rem))] overflow-hidden rounded-2xl border border-line-strong bg-overlay p-0 text-ink shadow-float"
    >
      <div className="flex items-center gap-2 border-b border-line px-4">
        <MagnifyingGlass aria-hidden className="size-4 text-ink-3" />
        <input
          autoFocus
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setActive((a) => Math.min(a + 1, commands.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setActive((a) => Math.max(a - 1, 0));
            } else if (e.key === "Enter") {
              commands[active]?.run();
              close();
            }
          }}
          placeholder="Go to a page, or paste an email ID"
          aria-label="Search"
          className="h-12 flex-1 bg-transparent text-sm outline-none placeholder:text-ink-3"
        />
        <Kbd>esc</Kbd>
      </div>
      <ul role="listbox" aria-label="Commands" className="max-h-80 overflow-y-auto p-2">
        {commands.length === 0 && <li className="px-3 py-6 text-center text-sm text-ink-3">No result</li>}
        {commands.map((c, i) => (
          <li key={c.id} role="option" aria-selected={i === active}>
            <button
              type="button"
              onMouseMove={() => setActive(i)}
              onClick={() => {
                c.run();
                close();
              }}
              className={cx(
                "flex h-9 w-full items-center gap-3 rounded-md px-3 text-left text-sm",
                i === active ? "bg-active text-ink" : "text-ink-2",
              )}
            >
              <c.icon aria-hidden className="size-4 text-ink-3" />
              <span className="flex-1 truncate">{c.label}</span>
              {c.hint && <span className="text-xs text-ink-3">{c.hint}</span>}
              {i === active && <ArrowRight aria-hidden className="size-4 text-ink-3" />}
            </button>
          </li>
        ))}
      </ul>
    </dialog>
  );
}
