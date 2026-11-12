import {
  Activity,
  Building2,
  FileText,
  Gauge,
  LogOut,
  MailX,
  Menu,
  Waypoints,
  X,
  ZapOff,
  type LucideIcon,
} from "lucide-react";
import { useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router";

import { useOverview, useSignOut } from "../api/hooks";
import { count } from "../lib/format";
import { Button, cx } from "./ui";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Number shown next to the label, when it needs attention. */
  badge?: number;
}

export function Layout() {
  const overview = useOverview();
  const signOut = useSignOut();
  const [open, setOpen] = useState(false);
  const location = useLocation();
  const [lastPath, setLastPath] = useState(location.pathname);
  // Navigating closes the menu on small screens.
  if (location.pathname !== lastPath) {
    setLastPath(location.pathname);
    setOpen(false);
  }

  const o = overview.data;
  const items: NavItem[] = [
    { to: "/", label: "Overview", icon: Gauge },
    { to: "/activity", label: "Activity", icon: Activity },
    { to: "/dead-letters", label: "Dead letters", icon: MailX, badge: o?.queue.dead },
    { to: "/failed-events", label: "Failed events", icon: ZapOff, badge: o?.events?.dead },
    { to: "/templates", label: "Templates", icon: FileText },
    { to: "/tenants", label: "Tenants", icon: Building2 },
    { to: "/routes", label: "Routes", icon: Waypoints },
  ];

  return (
    <div className="min-h-dvh">
      <a
        href="#main"
        className="fixed top-3 left-4 z-50 -translate-y-20 rounded-m bg-ink px-4 py-2 text-sm font-semibold text-card focus:translate-y-0"
      >
        Skip to content
      </a>
      <div className="airmail h-1.5" aria-hidden />

      <div className="lg:grid lg:min-h-[calc(100dvh-0.375rem)] lg:grid-cols-[15rem_1fr]">
        <aside className="border-b border-line bg-sunken lg:border-r lg:border-b-0">
          <div className="lg:sticky lg:top-0 lg:max-h-dvh lg:overflow-y-auto">
            <div className="flex items-center justify-between px-5 py-4 lg:py-6">
              <div>
                <p className="display text-2xl leading-none font-bold">Kourrier</p>
                <p className="stamp-type mt-1.5 text-[11px] text-red">Admin console</p>
              </div>
              <button
                type="button"
                className="rounded-m p-2 text-ink-2 hover:bg-page lg:hidden"
                aria-expanded={open}
                aria-controls="nav"
                aria-label={open ? "Close the menu" : "Open the menu"}
                onClick={() => setOpen(!open)}
              >
                {open ? <X className="size-5" /> : <Menu className="size-5" />}
              </button>
            </div>

            <nav id="nav" aria-label="Console" className={cx("px-3 pb-4 lg:block", !open && "hidden")}>
              <ul className="flex flex-col gap-0.5">
                {items.map(({ to, label, icon: Icon, badge }) => (
                  <li key={to}>
                    <NavLink
                      to={to}
                      end={to === "/"}
                      className={({ isActive }) =>
                        cx(
                          "flex items-center gap-3 rounded-m px-3 py-2 text-sm font-medium transition-colors",
                          isActive
                            ? "bg-card text-ink shadow-[0_1px_0_var(--c-line)]"
                            : "text-ink-2 hover:bg-page hover:text-ink",
                        )
                      }
                    >
                      <Icon aria-hidden className="size-4" />
                      <span className="flex-1">{label}</span>
                      {badge ? (
                        <span className="tabular rounded-s bg-failed px-1.5 py-0.5 font-mono text-[11px] text-card">
                          <span className="sr-only">, </span>
                          {count(badge)}
                          <span className="sr-only"> waiting</span>
                        </span>
                      ) : null}
                    </NavLink>
                  </li>
                ))}
              </ul>

              <div className="mt-6 border-t border-line px-3 pt-4 text-xs text-ink-3">
                {o && (
                  <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 font-mono">
                    <dt>version</dt>
                    <dd className="truncate text-ink-2">{o.version}</dd>
                    <dt>mailer</dt>
                    <dd className="text-ink-2">{o.mailer}</dd>
                    <dt>mode</dt>
                    <dd className="text-ink-2">{o.mode}</dd>
                  </dl>
                )}
                <Button
                  variant="ghost"
                  className="mt-4 -ml-3"
                  busy={signOut.isPending}
                  onClick={() => signOut.mutate()}
                >
                  <LogOut aria-hidden className="size-4" />
                  Sign out
                </Button>
              </div>
            </nav>
          </div>
        </aside>

        <main id="main" className="min-w-0 px-4 py-8 sm:px-8 lg:px-10 lg:py-10">
          <div className="mx-auto max-w-6xl">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
