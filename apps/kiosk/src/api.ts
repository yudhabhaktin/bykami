/*
 * The agent's local API.
 *
 * Every rule this file appears to enforce is enforced again on the server —
 * the take limit, the payment gate, the print count, the required consent.
 * Nothing here is a security boundary; it exists so the screen can explain
 * what is happening before the server has to refuse it.
 */

export type Source = "hotfolder" | "webcam";

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

export interface Session {
  id: string;
  state: "awaiting_payment" | "open" | "closed" | "abandoned";
  package_id: string;
  package_name: string;
  price_idr: number;
  template_id: string;
  print_copies: number;
  take_limit: number;
  takes: number;
  phone_given: boolean;
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
  packages: Package[];
  templates: Template[];
  session: Session | null;
  payment: Payment | null;
  media: { sheets_remaining: number; low: boolean };
  consent: { version: string; retention_days: number };
  flags: Record<string, boolean>;
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
      ...(init?.body instanceof Blob ? {} : { "Content-Type": "application/json" }),
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

  start: (packageId: string) =>
    request<{ session: Session; payment: Payment }>("/api/session", {
      method: "POST",
      body: JSON.stringify({ package_id: packageId }),
    }),

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
   * This session's frames. The template decides each frame's print_dpi, because
   * the cell it lands in is what it has to be scaled to fill.
   */
  photos: (templateId?: string) =>
    request<{ photos: Photo[] }>(
      templateId ? `/api/photos?template=${encodeURIComponent(templateId)}` : "/api/photos",
    ),

  photoURL: (id: string) => `/api/photos/${id}/file`,

  print: (templateId: string, photoIds: string[], copies: number) =>
    request<{ job: PrintJob }>("/api/print", {
      method: "POST",
      body: JSON.stringify({ template_id: templateId, photo_ids: photoIds, copies }),
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
