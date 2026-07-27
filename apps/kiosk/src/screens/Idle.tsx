import { useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError, rupiah } from "../api";

/**
 * The first screen: choose a package, then pay.
 *
 * Payment comes first because the booth is self-service. Nobody stands between
 * a stranger and the camera, so the QR code is the attendant — this reverses
 * the "No payment at the kiosk" decision in design/kiosk.md, which held only
 * while a human at the counter took the money.
 */
export function Idle({ state, refresh, setStep, setError }: ScreenProps) {
  const [busy, setBusy] = useState("");

  async function choose(packageId: string) {
    setBusy(packageId);
    setError("");
    try {
      await api.start(packageId);
      await refresh();
      setStep("pay");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal memulai sesi.");
    } finally {
      setBusy("");
    }
  }

  if (!state.flags.payments_enabled) {
    // A booth that cannot take money is a screen that says so, not one that
    // opens the shutter for free.
    return (
      <div className="grow center">
        <h1>Bayar di kasir dulu, ya</h1>
        <p className="muted">Pembayaran di booth belum aktif.</p>
      </div>
    );
  }

  return (
    <div className="grow">
      <h1>Pilih paket</h1>
      <p className="muted" style={{ marginBottom: "1.5rem" }}>
        Bayar dengan QRIS, lalu foto sepuasnya sampai batas take.
      </p>

      <div className="packages">
        {state.packages.map((p) => (
          <button
            key={p.id}
            className="package"
            onClick={() => void choose(p.id)}
            disabled={busy !== ""}
            aria-pressed={busy === p.id}
          >
            <h2>{p.name}</h2>
            <span className="price">{rupiah(p.price_idr)}</span>
            <span className="muted small">
              Maksimal {p.take_limit}x take · {p.print_copies} cetak
            </span>
            <span className="muted small">± {p.duration_minutes} menit</span>
          </button>
        ))}
      </div>
    </div>
  );
}
