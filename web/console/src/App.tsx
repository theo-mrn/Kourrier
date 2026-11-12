import { Route, Routes } from "react-router";

import { useSession } from "./api/hooks";
import { Layout } from "./components/Layout";
import { ErrorNotice } from "./components/ui";
import { Activity } from "./pages/Activity";
import { DeadLetters } from "./pages/DeadLetters";
import { Email } from "./pages/Email";
import { FailedEvents } from "./pages/FailedEvents";
import { Login } from "./pages/Login";
import { NotFound } from "./pages/NotFound";
import { Overview } from "./pages/Overview";
import { RoutesPage } from "./pages/Routes";
import { Templates } from "./pages/Templates";
import { Tenants } from "./pages/Tenants";

export function App() {
  const session = useSession();

  if (session.isPending) {
    return <main className="grid min-h-dvh place-items-center" role="status" aria-label="Opening the console" />;
  }
  if (session.error) {
    return (
      <main className="mx-auto max-w-md px-4 py-20">
        <ErrorNotice error={session.error} />
      </main>
    );
  }
  if (!session.data) return <Login />;

  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Overview />} />
        <Route path="activity" element={<Activity />} />
        <Route path="emails/:id" element={<Email />} />
        <Route path="dead-letters" element={<DeadLetters />} />
        <Route path="failed-events" element={<FailedEvents />} />
        <Route path="templates" element={<Templates />} />
        <Route path="tenants" element={<Tenants />} />
        <Route path="routes" element={<RoutesPage />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
