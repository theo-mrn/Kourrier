import { Link } from "react-router";

import { EmptyState } from "../components/ui";

export function NotFound() {
  return (
    <EmptyState title="Return to sender">
      This page does not exist.{" "}
      <Link to="/" className="font-semibold text-blue hover:underline">
        Back to the overview
      </Link>
    </EmptyState>
  );
}
