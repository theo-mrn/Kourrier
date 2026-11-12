/**
 * A short, readable summary of a delivery error. The full message (a Go
 * error chain) stays available as a tooltip and on the email page.
 */
export function summarizeError(message: string): string {
  const m = message.toLowerCase();
  if (m.includes("invalid template") || m.includes("template not found")) return "Template error";
  if (m.includes("not a valid e-mail address") || m.includes("invalid request")) return "Invalid email";
  if (m.includes("timeout") || m.includes("deadline exceeded")) return "Provider timeout";
  if (/\b5\d\d\b/.test(m) || m.includes("rejected")) return "Rejected by the provider";
  if (/\b4\d\d\b/.test(m) || m.includes("throttl")) return "Provider busy";
  if (m.includes("connection refused") || m.includes("no such host")) return "Provider unreachable";
  return "Delivery error";
}
