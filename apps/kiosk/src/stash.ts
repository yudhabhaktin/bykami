/*
 * The few things about a session the server does not hold.
 *
 * Almost nothing belongs here. The flow is derived from the server's session
 * state on every load precisely so that a refresh, a browser restart or a power
 * cut resumes where the customer was — and anything kept only in React state is
 * lost at exactly that moment.
 *
 * Two values have nowhere better to live. The cut choice is not committed until
 * a sheet is queued, and the photo session's deadline is a clock the agent
 * deliberately does not run: it refuses no frames on time, because cutting
 * somebody off mid-pose with their money already taken is worse than a session
 * running over. A deadline held only in memory would hand out a fresh five
 * minutes on every reload, which is not a limit.
 *
 * Keyed by session id, so the next customer inherits nothing. sessionStorage
 * rather than localStorage for the same reason: a value that outlived the tab
 * would be a stranger's choice applied to somebody else's print.
 */

function key(sessionId: string, name: string): string {
  return `bykami:${sessionId}:${name}`;
}

export function remember(sessionId: string, name: string, value: string): void {
  try {
    sessionStorage.setItem(key(sessionId, name), value);
  } catch {
    // Storage disabled or full. The booth keeps working on its in-memory copy;
    // the only cost is that a reload forgets, which is where it started.
  }
}

export function recall(sessionId: string, name: string): string | null {
  try {
    return sessionStorage.getItem(key(sessionId, name));
  } catch {
    return null;
  }
}
