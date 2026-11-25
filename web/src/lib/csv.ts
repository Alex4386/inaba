/**
 * Splits CSV or TSV text into rows of fields. Quoted fields may hold the separator,
 * line breaks and doubled quotes; a trailing line break does not make an empty row.
 */
export function parseDelimited(text: string, separator: "," | "\t"): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c !== '"') field += c;
      else if (text[i + 1] === '"') (field += '"'), i++;
      else quoted = false;
    } else if (c === '"' && field === "") quoted = true;
    else if (c === separator) row.push(field), (field = "");
    else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i++;
      row.push(field), rows.push(row), (row = []), (field = "");
    } else field += c;
  }
  if (field !== "" || row.length > 0) row.push(field), rows.push(row);
  return rows;
}
