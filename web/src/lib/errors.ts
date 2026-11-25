import { ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";

/** The message to show a person for a failed call. */
export function errorText(error: unknown): string {
  return error instanceof ConnectError ? error.rawMessage : String(error);
}

export const showError = (error: unknown) => void toast.error(errorText(error));
