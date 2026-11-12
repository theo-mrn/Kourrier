import {
  Buildings,
  ChartBar,
  EnvelopeSimple,
  FileText,
  Path,
  Tray,
  WarningOctagon,
  type Icon,
} from "@phosphor-icons/react";

export interface NavItem {
  to: string;
  label: string;
  icon: Icon;
  /** Entries waiting for an operator. */
  counter?: number;
}

export function navGroups(dead?: number, failedEvents?: number): { label?: string; items: NavItem[] }[] {
  return [
    {
      items: [
        { to: "/", label: "Dashboard", icon: ChartBar },
        { to: "/activity", label: "Emails", icon: EnvelopeSimple },
      ],
    },
    {
      label: "Needs attention",
      items: [
        { to: "/dead-letters", label: "Dead letters", icon: Tray, counter: dead },
        { to: "/failed-events", label: "Failed events", icon: WarningOctagon, counter: failedEvents },
      ],
    },
    {
      label: "Configuration",
      items: [
        { to: "/templates", label: "Templates", icon: FileText },
        { to: "/tenants", label: "Tenants", icon: Buildings },
        { to: "/routes", label: "Routes", icon: Path },
      ],
    },
  ];
}
