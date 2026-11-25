// What the composer checks before anything leaves the browser: inaba's own slash commands,
// and the attachment caps the host enforces, so a refused upload is caught before it is sent.

/** inaba's own slash commands, handled by the thread view instead of the agent. */
export const builtinCommands = ["side", "btw", "clear", "model", "compact"] as const;
export type BuiltinCommand = (typeof builtinCommands)[number];
/** Commands that need text after them, so picking one from the menu waits for it instead of running. */
export const commandTakesText: Record<BuiltinCommand, boolean> = { side: true, btw: true, clear: false, model: false, compact: false };

/** The built-in command a message is, with the text after it; undefined for an ordinary prompt. */
export function parseBuiltin(message: string, available: readonly BuiltinCommand[]): { command: BuiltinCommand; arg: string } | undefined {
  const text = message.trim();
  if (!text.startsWith("/")) return;
  // The name runs to the first space or line break, and its text is whatever follows.
  const gap = text.search(/\s/);
  const end = gap < 0 ? text.length : gap;
  const command = available.find((name) => name === text.slice(1, end));
  return command && { command, arg: text.slice(end).trim() };
}

// The host's caps (internal/host/message.go); kept equal so the error is shown here and not after the upload.
export const maxAttachments = 20;
export const maxImageBytes = 5 << 20;
export const maxFileBytes = 10 << 20;
export const maxTotalBytes = 20 << 20;

/** Why a file cannot join the attachments already held (their count and data size), or undefined when it can. */
export function attachmentProblem(file: { size: number; type: string }, held: { count: number; bytes: number }): "empty" | "tooMany" | "imageTooLarge" | "fileTooLarge" | "totalTooLarge" | undefined {
  const image = file.type.startsWith("image/");
  // The host takes data attachments only with bytes in them.
  if (file.size === 0) return "empty";
  if (held.count >= maxAttachments) return "tooMany";
  if (image && file.size > maxImageBytes) return "imageTooLarge";
  if (!image && file.size > maxFileBytes) return "fileTooLarge";
  if (held.bytes + file.size > maxTotalBytes) return "totalTooLarge";
}

/** A file size as people read it: "820 B", "12 kB", "3.4 MB". */
export function formatBytes(bytes: number, locale: string): string {
  // Intl's short "byte" reads "820 byte", so plain bytes get the symbol by hand.
  if (bytes < 1000) return `${bytes.toLocaleString(locale)} B`;
  const [value, unit] = bytes < 1e6 ? [bytes / 1e3, "kilobyte"] : [bytes / 1e6, "megabyte"];
  return new Intl.NumberFormat(locale, { style: "unit", unit, unitDisplay: "short", maximumFractionDigits: value < 10 ? 1 : 0 }).format(value);
}
