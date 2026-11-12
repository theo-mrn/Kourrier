import { KeyRound } from "lucide-react";
import { useState, type FormEvent } from "react";

import { ApiError } from "../api/client";
import { useSignIn } from "../api/hooks";
import { Button, Field, inputClass } from "../components/ui";

export function Login() {
  const [token, setToken] = useState("");
  const signIn = useSignIn();

  function submit(e: FormEvent) {
    e.preventDefault();
    if (token.trim()) signIn.mutate(token.trim());
  }

  const error =
    signIn.error instanceof ApiError && signIn.error.unauthorized
      ? "This token is not valid."
      : signIn.error
        ? "Kourrier could not be reached. Try again."
        : null;

  return (
    <main className="grid min-h-dvh place-items-center px-4 py-10">
      <div className="w-full max-w-md overflow-hidden rounded-l border border-line bg-card shadow-sm">
        <div className="airmail h-2" aria-hidden />
        <form onSubmit={submit} className="p-8" noValidate>
          <p className="stamp-type text-xs text-red">Admin console</p>
          <h1 className="display mt-2 text-4xl font-bold">Kourrier</h1>
          <p className="mt-2 text-sm text-ink-2">
            Sign in with the admin token, the content of the file set in{" "}
            <code className="font-mono text-[13px]">KOURRIER_CONSOLE_TOKEN_FILE</code>.
          </p>

          <Field label="Admin token" className="mt-6">
            <input
              type="password"
              name="token"
              autoComplete="current-password"
              required
              autoFocus
              value={token}
              onChange={(e) => setToken(e.target.value)}
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? "login-error" : undefined}
              className={inputClass}
            />
          </Field>
          {error && (
            <p id="login-error" role="alert" className="mt-2 text-sm text-failed">
              {error}
            </p>
          )}

          <Button
            type="submit"
            variant="primary"
            className="mt-6 w-full"
            busy={signIn.isPending}
            disabled={!token.trim()}
          >
            <KeyRound aria-hidden className="size-4" />
            Sign in
          </Button>
        </form>
      </div>
    </main>
  );
}
