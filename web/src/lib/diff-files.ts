// shortcut: guesses test files from common naming conventions only, upgrade to per-project patterns if a repository names its tests otherwise.
const TEST_PATH = /(^|\/)(tests?|__tests__)\/|_test\.go$|\.test\.[cm]?[jt]sx?$|\.spec\.[^/]+$/;

/** Whether a changed file looks like a test, so the changes panel can leave tests out. */
export function isTestPath(path: string): boolean {
  return TEST_PATH.test(path);
}
