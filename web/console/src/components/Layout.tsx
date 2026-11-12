import { List as ListIcon, MagnifyingGlass, SignOut, X } from "@phosphor-icons/react";
import { useEffect, useState, type ReactNode } from "react";
import { NavLink, Outlet, useLocation } from "react-router";

import { useOverview, useSignOut } from "../api/hooks";
import { count } from "../lib/format";
import { CommandMenu } from "./CommandMenu";
import { navGroups } from "./nav";
import { cx } from "./ui";

/** The product mark. */
export function Mark({ size = "md" }: { size?: "md" | "lg" }) {
  return (
    <span
      aria-hidden
      className={cx(
        "grid place-items-center rounded-lg border border-line-strong bg-overlay font-semibold text-ink shadow-[inset_0_1px_0_rgb(255_255_255/0.06)]",
        size === "lg" ? "size-11 text-lg" : "size-6 text-xs",
      )}
    >
      K
    </span>
  );
}

/** A round icon button, as in Linear's headers. */
export function IconButton({
  label,
  onClick,
  children,
  disabled,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      className="grid size-8 place-items-center rounded-full border border-line text-ink-2 transition-colors duration-200 hover:border-line-strong hover:bg-hover hover:text-ink disabled:opacity-40"
    >
      {children}
    </button>
  );
}

function Sidebar({ onSearch }: { onSearch: () => void }) {
  const overview = useOverview();
  const signOut = useSignOut();
  const o = overview.data;

  return (
    <div className="flex h-full flex-col px-3 pt-4 pb-3">
      <div className="flex items-center gap-3 pr-1 pl-3">
        <Mark />
        <span className="flex-1 text-sm font-semibold">Kourrier</span>
        <IconButton label="Search (⌘K)" onClick={onSearch}>
          <MagnifyingGlass aria-hidden className="size-4" />
        </IconButton>
        <IconButton label="Sign out" onClick={() => signOut.mutate()} disabled={signOut.isPending}>
          <SignOut aria-hidden className="size-4" />
        </IconButton>
      </div>

      <nav aria-label="Console" className="mt-6 flex flex-col gap-6">
        {navGroups(o?.queue.dead, o?.events?.dead).map((group, i) => (
          <div key={i}>
            {group.label && <p className="mb-2 px-3 text-xs text-ink-4">{group.label}</p>}
            <ul className="flex flex-col gap-0.5">
              {group.items.map(({ to, label, icon: I, counter }) => (
                <li key={to}>
                  <NavLink
                    to={to}
                    end={to === "/"}
                    className={({ isActive }) =>
                      cx(
                        "flex h-9 items-center gap-3 rounded-lg px-3 text-sm transition-colors duration-200 ease-out-soft",
                        isActive ? "bg-active text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
                      )
                    }
                  >
                    <I aria-hidden className="size-4.5 opacity-80" />
                    <span className="flex-1">{label}</span>
                    {counter ? (
                      <span className="tabular rounded-full bg-critical/15 px-2 text-xs text-critical">
                        <span className="sr-only">, </span>
                        {count(counter)}
                        <span className="sr-only"> waiting</span>
                      </span>
                    ) : null}
                  </NavLink>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </nav>
    </div>
  );
}

export function Layout() {
  const [menu, setMenu] = useState(false);
  const [search, setSearch] = useState(false);
  const location = useLocation();
  const [lastPath, setLastPath] = useState(location.pathname);
  // Navigating closes the menu on small screens.
  if (location.pathname !== lastPath) {
    setLastPath(location.pathname);
    setMenu(false);
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setSearch((open) => !open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  return (
    <div className="h-dvh">
      <a
        href="#main"
        className="fixed top-2 left-2 z-50 -translate-y-20 rounded-lg bg-accent px-3 py-2 text-sm text-on-accent focus:translate-y-0"
      >
        Skip to content
      </a>

      {/* The app fills the screen: the sidebar on the window background, the
          content in a panel set slightly inside it. */}
      <div className="surface-window flex h-full flex-col overflow-hidden lg:flex-row">
        <aside className="hidden w-64 shrink-0 lg:block">
          <Sidebar onSearch={() => setSearch(true)} />
        </aside>

        {/* Small screens: a bar with the menu, the sidebar as a full height panel. */}
        <div className="flex h-14 shrink-0 items-center justify-between border-b border-line px-4 lg:hidden">
          <span className="flex items-center gap-3">
            <Mark />
            <span className="text-sm font-semibold">Kourrier</span>
          </span>
          <IconButton label={menu ? "Close the menu" : "Open the menu"} onClick={() => setMenu(!menu)}>
            {menu ? <X className="size-4" /> : <ListIcon className="size-4" />}
          </IconButton>
        </div>
        {menu && (
          <div className="surface-window fixed inset-x-0 top-14 bottom-0 z-40 lg:hidden">
            <Sidebar onSearch={() => setSearch(true)} />
          </div>
        )}

        <main
          id="main"
          className="surface-panel flex min-h-0 flex-1 flex-col overflow-hidden lg:my-2 lg:mr-2 lg:rounded-xl lg:border lg:border-line"
        >
          <Outlet />
        </main>
      </div>

      <CommandMenu open={search} onClose={() => setSearch(false)} />
    </div>
  );
}

/**
 * A page of the console: a header with the title and the page actions, and
 * a scrolling body with generous margins.
 */
export function Page({
  title,
  description,
  actions,
  children,
  padded = true,
}: {
  title: ReactNode;
  description?: string;
  actions?: ReactNode;
  children: ReactNode;
  padded?: boolean;
}) {
  return (
    <>
      <header className="flex h-14 shrink-0 items-center justify-between gap-4 border-b border-line px-6">
        <div className="flex min-w-0 items-baseline gap-3">
          <h1 className="truncate text-sm font-medium">{title}</h1>
          {description && <p className="hidden truncate text-sm text-ink-3 md:block">{description}</p>}
        </div>
        {actions && <div className="flex items-center gap-2">{actions}</div>}
      </header>
      <div className={cx("animate-rise flex-1 overflow-y-auto", padded && "px-8 py-8")}>{children}</div>
    </>
  );
}
