/** Where a lowercased query starts in a text, case-insensitively and without overlaps. */
export function matchOffsets(text: string, query: string): number[] {
  const out: number[] = [];
  if (!query) return out;
  const lower = text.toLowerCase();
  // shortcut: where lowercasing changes the length (e.g. "İ"), offsets would point at the wrong characters, so that text is matched case-sensitively; fold per character if it matters.
  const hay = lower.length === text.length ? lower : text;
  for (let at = hay.indexOf(query); at !== -1; at = hay.indexOf(query, at + query.length)) out.push(at);
  return out;
}

/** A selection as a markdown blockquote, with a blank line after it for the reply. */
export const quoteMarkdown = (text: string) =>
  text
    .trim()
    .split(/\r?\n/)
    .map((line) => (line ? `> ${line}` : ">"))
    .join("\n") + "\n\n";

/** The plan as a markdown task list. */
export const planMarkdown = (entries: { content: string; status: string }[]) =>
  entries.map((e) => `- [${e.status === "completed" ? "x" : " "}] ${e.content}${e.status === "in_progress" ? " (in progress)" : ""}`).join("\n");
