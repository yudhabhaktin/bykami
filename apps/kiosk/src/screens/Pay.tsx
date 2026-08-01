import { useEffect, useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError, rupiah } from "../api";
import { countdown, QRCanvas } from "../QRCanvas";

/** How often the booth asks the gateway whether the money arrived. */
const POLL_MS = 2000;

/**
 * The QR screen.
 *
 * Settlement is polled, never pushed. The booth is at http://localhost with no
 * inbound path, so a gateway webhook could not reach it even if one were
 * configured — and a lost callback is a slow answer here rather than a stuck
 * screen with a paying customer in front of it.
 */
export function Pay({ state, refresh, setStep, setError }: ScreenProps) {
  const [remaining, setRemaining] = useState(state.payment?.expires_in ?? 0);
  const payload = state.payment?.qr_payload ?? "";

  // The countdown is shown because an unexplained expiry looks like a broken
  // machine rather than a code that timed out.
  useEffect(() => {
    const t = setInterval(() => setRemaining((n) => Math.max(0, n - 1)), 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    let cancelled = false;

    const tick = async () => {
      try {
        const { payment, session } = await api.pollPayment();
        if (cancelled) return;
        setRemaining(payment.expires_in);

        if (payment.state === "settled" && session.state === "open") {
          await refresh();
          // What they bought, before what it looks like: the cut decides what
          // comes out of the machine, and the frame screen is the choice after
          // it.
          setStep("session");
          return;
        }
        if (payment.state === "expired" || payment.state === "failed") {
          setError("Waktu pembayaran habis. Silakan mulai lagi.");
          await api.cancel().catch(() => undefined);
          await refresh();
          setStep("attract");
        }
      } catch {
        // A failed poll is not a failed payment. The customer is standing
        // right there, so the screen stays put and asks again.
      }
    };

    const t = setInterval(() => void tick(), POLL_MS);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [refresh, setStep, setError]);

  async function cancel() {
    await api.cancel().catch(() => undefined);
    await refresh();
    setStep("attract");
  }

  return (
    <div className="grow center">
      <h1>Scan untuk bayar</h1>
      <p className="muted">
        {state.session?.package_name} · {rupiah(state.payment?.amount_idr ?? 0)}
      </p>

      <QRCanvas payload={payload} onError={setError} />

      <p className="muted">Berlaku {countdown(remaining)}</p>

      <div className="actions" style={{ maxWidth: "32rem", width: "100%" }}>
        <button className="btn ghost" onClick={() => void cancel()}>
          Batal
        </button>

        {/*
          Only rendered when the simulated provider is selected — the route it
          calls is a 404 otherwise, so a booth that could take real money has no
          button that skips paying.
        */}
        {state.flags.payments_simulated && (
          <button
            className="btn secondary"
            onClick={() =>
              void api
                .simulatePayment()
                .catch((err) =>
                  setError(err instanceof ApiError ? err.message : "Gagal simulasi."),
                )
            }
          >
            Simulasikan pembayaran
          </button>
        )}
      </div>
    </div>
  );
}
