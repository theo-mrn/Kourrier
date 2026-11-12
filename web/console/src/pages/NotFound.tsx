import { Compass } from "@phosphor-icons/react";
import { Link } from "react-router";

import { Page } from "../components/Layout";
import { EmptyState } from "../components/ui";

export function NotFound() {
  return (
    <Page title="Not found">
      <EmptyState icon={Compass} title="This page does not exist">
        <Link to="/" className="text-accent hover:underline">
          Back to the dashboard
        </Link>
      </EmptyState>
    </Page>
  );
}
