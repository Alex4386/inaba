export { cn } from "cn"

/** The names a host's path goes through, whichever slashes its system writes. */
export function pathNames(path: string): string[] {
  return path
    .replaceAll("\\", "/")
    .split("/")
    .filter((name) => name !== "");
}

/** A long folder path cut down to its last two names, which are what tell folders apart. */
export function shortPath(path: string): string {
  const names = pathNames(path);
  const tail = names.slice(-2);
  return tail.length < names.length ? ["…", ...tail].join("/") : path;
}
