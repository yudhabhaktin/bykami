/*
 * The agent's local API.
 *
 * Every rule this file appears to enforce is enforced again on the server —
 * the take limit, the payment gate, the print count, the required consent.
 * Nothing here is a security boundary; it exists so the screen can explain
 * what is happening before the server has to refuse it.
 */

export type Source = "hotfolder" | "webcam" | "hybrid";

/** Whether this source shows a live camera. Hybrid previews without capturing
 *  from it: the printed frame arrives through the hot folder instead. */
export function previews(s: Source): boolean {
  return s === "webcam" || s === "hybrid";
}

export interface Package {
  id: string;
  name: string;
  price_idr: number;
  duration_minutes: number;
  take_limit: number;
  template_id: string;
  print_copies: number;
}

export interface Cell {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface Template {
  id: string;
  name: string;
  layout: string;
  cells: Cell[];
  /** Sheet size in pixels at 300 dpi. Cells are in the same space. */
  sheet: [number, number];
  /** Frame artwork URLs, empty when this template has none. */
  overlay: string;
  background: string;
}

/**
 * A colour matrix, in feColorMatrix order: four rows of five.
 *
 * Served by the agent rather than written down here, and that is the point.
 * The printed sheet is filtered in Go at compose time; if the browser had its
 * own copy of these numbers the screen and the paper could disagree, and the
 * customer would find out after paying. There is one copy, in
 * agent/internal/compose/filter.go, and the browser is handed it.
 */
export interface Filter {
  id: string;
  name: string;
  /** Absent on the identity filter, which is the default. */
  matrix?: number[];
}

export interface Session {
  id: string;
  state: "awaiting_payment" | "open" | "closed" | "abandoned";
  package_id: string;
  package_name: string;
  price_idr: number;
  template_id: string;
  /**
   * How many sheets this session may take away in total: the one print it
   * includes, plus one for every reprint that has actually been paid for. Grows
   * only when a reprint payment settles.
   */
  print_copies: number;
  /** How many of print_copies have been claimed. Server-held, so a refresh
   *  cannot hand the allowance out twice. */
  prints_done: number;
  take_limit: number;
  takes: number;
  phone_given: boolean;
  /**
   * The download page for this session, or "" when this booth has no public
   * hostname and therefore nothing a phone could reach. Empty is the normal
   * case on a real booth, and the delivery screen offers WhatsApp alone rather
   * than a QR code that scans to nothing.
   */
  share_url: string;
}

export interface Payment {
  id: string;
  state: "pending" | "settled" | "expired" | "failed";
  amount_idr: number;
  qr_payload: string;
  expires_in: number;
}

export interface Photo {
  id: string;
  width: number;
  height: number;
  captured_at: number;
  print_dpi: number;
}

export interface PrintJob {
  id: string;
  state: "queued" | "printing" | "done" | "failed";
  layout: string;
  copies: number;
  error?: string;
}

export interface State {
  source: Source;
  /** Label substring naming which video device to preview. Empty takes the
   *  browser's default, which on a booth PC is the built-in webcam. */
  camera: string;
  /** Whether the agent can fire the camera itself. False is the booth where
   *  somebody presses the shutter by hand, and it keeps the single-shot
   *  button — an automatic countdown with no trigger at the end of it is a
   *  booth that photographs nobody. */
  shutter: boolean;
  packages: Package[];
  templates: Template[];
  filters: Filter[];
  session: Session | null;
  payment: Payment | null;
  media: { sheets_remaining: number; low: boolean };
  consent: { version: string; retention_days: number };
  flags: Record<string, boolean>;
  /** What one extra print costs. Served, so the price on the button is the
   *  price the QR code will charge. */
  reprint_idr: number;
}

/** The message the booth screen shows. Indonesian, because a customer reads it. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      // FormData carries a generated multipart boundary, so the browser has to
      // write the header itself. Setting it here would name a boundary that is
      // not in the body, and the server would find no parts at all.
      ...(init?.body instanceof Blob || init?.body instanceof FormData
        ? {}
        : { "Content-Type": "application/json" }),
      ...init?.headers,
    },
  });

  if (!res.ok) {
    let message = "Terjadi kesalahan. Panggil petugas.";
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // A non-JSON error body is still an error; the default message stands.
    }
    throw new ApiError(message, res.status);
  }
  return (await res.json()) as T;
}

export const api = {
  state: () => request<State>("/api/state"),

  /**
   * Opens the booth's one session and mints the QR code for it.
   *
   * Nothing is named. There is a single session to buy, so the screen has
   * nothing to choose and the price lives at the gateway with the server that
   * charges it — a package id sent from here would only be a second place for
   * the screen and the charge to disagree.
   */
  start: () =>
    request<{ session: Session; payment: Payment }>("/api/session", {
      method: "POST",
      body: JSON.stringify({}),
    }),

  /**
   * Puts a second QR code up, for one more sheet.
   *
   * Idempotent while a code is live: tapping again returns the charge already
   * on screen rather than minting another one the customer could also scan.
   */
  reprint: () =>
    request<{ session: Session; payment: Payment }>("/api/reprint", { method: "POST" }),

  /** Settlement is polled: the booth has no inbound path for a gateway webhook. */
  pollPayment: () => request<{ session: Session; payment: Payment }>("/api/payment"),

  /** Only exists while the simulated provider is selected. */
  simulatePayment: () => request<{ ok: true }>("/api/payment/simulate", { method: "POST" }),

  cancel: () => request<{ ok: true }>("/api/session/cancel", { method: "POST" }),
  close: () => request<{ ok: true }>("/api/session/close", { method: "POST" }),

  capture: (frame: Blob) =>
    request<{ photo: Photo }>("/api/capture", {
      method: "POST",
      body: frame,
      headers: { "Content-Type": "image/jpeg" },
    }),

  /**
   * Fires a tethered camera, where there are no pixels to send.
   *
   * The answer is 202 and not a photograph: the frame is still travelling down
   * the USB cable and becomes a take when the hot-folder watcher finds it. What
   * matters here is that the request is *checked* — a booth that ignored the
   * status would count down over a camera that refused to fire and cheerfully
   * carry on to the next pose.
   */
  fire: () => request<{ awaiting_file: boolean }>("/api/capture", { method: "POST" }),

  /**
   * The seconds of camera around one shutter, which become the frame's moving
   * version on the download page.
   *
   * A separate request from the frame it belongs to, and posted after it. This
   * is twenty times the bytes, and /api/capture is on the shutter path — the
   * one place in the booth where latency is the product.
   */
  clip: (photoId: string, frames: Blob[]) => {
    const body = new FormData();
    frames.forEach((f, i) => body.append("frame", f, `${i}.jpg`));
    return request<{ ok: true }>(`/api/capture/${photoId}/clip`, { method: "POST", body });
  },

  /**
   * This session's frames. The template decides each frame's print_dpi, because
   * the cell it lands in is what it has to be scaled to fill.
   */
  photos: (templateId?: string) =>
    request<{ photos: Photo[] }>(
      templateId ? `/api/photos?template=${encodeURIComponent(templateId)}` : "/api/photos",
    ),

  photoURL: (id: string) => `/api/photos/${id}/file`,

  print: (
    templateId: string,
    photoIds: string[],
    copies: number,
    filter: string,
    cut: boolean,
  ) =>
    request<{ job: PrintJob }>("/api/print", {
      method: "POST",
      body: JSON.stringify({
        template_id: templateId,
        photo_ids: photoIds,
        copies,
        filter,
        cut,
      }),
    }),

  printStatus: (id: string) => request<{ job: PrintJob }>(`/api/print/${id}`),

  delivery: (phone: string, consent: boolean, marketing: boolean) =>
    request<{ ok: true }>("/api/delivery", {
      method: "POST",
      body: JSON.stringify({ phone, consent, marketing }),
    }),
};

export function rupiah(n: number): string {
  return "Rp " + n.toLocaleString("id-ID");
}
