/*
 * Client-side timings for the shutter path.
 *
 * The booth's smoothness is decided almost entirely in this browser: the agent
 * is on loopback, and the two things a customer waits for — the camera opening
 * and the frame encoding — never touch it. Measuring on the server would report
 * the fastest part of the system.
 *
 * Off unless asked for. `?perf=1` turns it on and it sticks for the tab, so the
 * flag survives the navigation the access token performs on a test deployment.
 */

const KEY = "bykami:perf";

export type Timings = Record<string, number>;

let enabled = false;

export function initPerf(): boolean {
  const params = new URLSearchParams(window.location.search);
  if (params.get("perf") === "1") sessionStorage.setItem(KEY, "1");
  if (params.get("perf") === "0") sessionStorage.removeItem(KEY);
  enabled = sessionStorage.getItem(KEY) === "1";
  return enabled;
}

export function perfEnabled(): boolean {
  return enabled;
}

/** Times fn and records the result under name. Returns whatever fn returns. */
export async function timed<T>(into: Timings, name: string, fn: () => Promise<T>): Promise<T> {
  if (!enabled) return fn();
  const t0 = performance.now();
  try {
    return await fn();
  } finally {
    into[name] = Math.round(performance.now() - t0);
  }
}

/** Records a span that was measured by hand, for the ones that are not a call. */
export function record(into: Timings, name: string, ms: number) {
  if (enabled) into[name] = Math.round(ms);
}

/**
 * A readable label for each measurement. Kept here rather than in the overlay so
 * that adding a timing to a screen adds it to the display too.
 */
export const labels: Record<string, string> = {
  camera: "kamera siap",
  encode: "encode JPEG",
  bytes: "ukuran frame",
  upload: "kirim ke agent",
  refresh: "refresh state",
  shutter: "total rana",
  filmstrip: "muat filmstrip",
};
