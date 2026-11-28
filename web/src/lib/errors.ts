import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";
import { translateNow } from "@/lib/i18n";

// What a browser says, whole, when a call never reached anyone (Chrome, Safari, Firefox), and what the direct
// connection says when it drops: matched in full, so a host's own message that mentions a network is still shown.
const NETWORK = /^(failed to fetch|load failed|networkerror when attempting to fetch resource\.|network error|the direct connection (failed|closed before the host answered)\.)$/i;
// What the server's relay says when the host is away, as while it restarts.
const RELAY = /^the host (is not connected|did not answer)$/;

/** Whether a call failed for want of the host or server, not for anything it said: worth waiting out, not reading. */
export function unreachable(error: unknown): boolean {
  if (error instanceof ConnectError) return (error.code === Code.Unavailable && RELAY.test(error.rawMessage)) || ((error.code === Code.Unknown || error.code === Code.Unavailable) && NETWORK.test(error.rawMessage));
  return error instanceof TypeError && NETWORK.test(error.message);
}

/** The message to show a person for a failed call. */
export function errorText(error: unknown): string {
  if (unreachable(error)) return translateNow("errors.unreachable");
  return error instanceof ConnectError ? error.rawMessage : String(error);
}

// The same message twice is one toast: a host restarting fails many calls at once.
export const showError = (error: unknown) => {
  const text = errorText(error);
  toast.error(text, { id: text });
};
