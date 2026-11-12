import { useState, type FormEvent } from "react";

import { ApiError } from "../api/client";
import { useSignIn } from "../api/hooks";
import { Mark } from "../components/Layout";
import { Button, inputClass } from "../components/ui";

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
    <main className="grid min-h-dvh place-items-center px-4">
      <div className="animate-rise w-full max-w-xs text-center">
        <div className="flex justify-center">
          <Mark size="lg" />
        </div>
        <h1 className="mt-6 text-xl font-medium tracking-tight">Sign in to Kourrier</h1>
        <p className="mt-2 text-sm text-ink-3">Use the admin token of this instance.</p>

        <form onSubmit={submit} noValidate className="mt-8 text-left">
          <label htmlFor="token" className="mb-2 block text-xs text-ink-3">
            Admin token
          </label>
          <input
            id="token"
            type="password"
            name="token"
            autoComplete="current-password"
            required
            autoFocus
            value={token}
            onChange={(e) => setToken(e.target.value)}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? "login-error" : undefined}
            className={`${inputClass} h-10`}
          />
          {error && (
            <p id="login-error" role="alert" className="mt-2 text-sm text-critical">
              {error}
            </p>
          )}
          <Button
            type="submit"
            variant="primary"
            className="mt-4 h-10 w-full"
            busy={signIn.isPending}
            disabled={!token.trim()}
          >
            Continue
          </Button>
        </form>
        <p className="mt-8 text-xs text-ink-3">
          The token is the content of the file set in <span className="font-mono">KOURRIER_CONSOLE_TOKEN_FILE</span>.
        </p>
      </div>
    </main>
  );
}
