/**
 * Sends inputs one at a time, in the order they were made. An input that can join the one still
 * waiting in line (join gives the two as one) does, rather than queue behind it: sent one by one,
 * moves and scrolls made faster than a round trip piled up, and a click then waited behind them.
 */
export function inputLine<E>(post: (event: E) => Promise<unknown>, join: (waiting: E, next: E) => E | undefined, joinable: (event: E) => boolean) {
  let line: Promise<unknown> = Promise.resolve();
  let waiting: { event: E } | undefined;
  return (event: E) => {
    const merged = waiting && join(waiting.event, event);
    if (waiting && merged !== undefined) {
      waiting.event = merged;
      return line;
    }
    const mine = { event };
    waiting = joinable(event) ? mine : undefined;
    line = line.then(() => {
      if (waiting === mine) waiting = undefined;
      return post(mine.event).catch(() => {});
    });
    return line;
  };
}
