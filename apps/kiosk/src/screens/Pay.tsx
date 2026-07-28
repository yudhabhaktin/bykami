import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";

import type { ScreenProps } from "../App";
import { api, ApiError, rupiah } from "../api";

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
  const canvas = useRef<HTMLCanvasElement>(null);
  const [remaining, setRemaining] = useState(state.payment?.expires_in ?? 0);
  const payload = state.payment?.qr_payload ?? "";

  // The QR is drawn locally from the payload the gateway returned. Nothing
  // fetches an image: the booth has to work with the network down, and a QR
  // code served from someone else's host is a dependency in the middle of the
  // one screen where money changes hands.
  useEffect(() => {
    if (!canvas.current || !payload) return;
    void QRCode.toCanvas(canvas.current, payload, {
      errorCorrectionLevel: "M",
      margin: 1,
      width: 512,
    }).catch(() => setError("Gagal menampilkan QR. Panggil petugas."));
  }, [payload, setError]);

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
          setStep("frame");
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

  const mins = Math.floor(remaining / 60);
  const secs = String(remaining % 60).padStart(2, "0");

  return (
    <div className="grow center">
      <h1>Scan untuk bayar</h1>
      <p className="muted">
        {state.session?.package_name} · {rupiah(state.payment?.amount_idr ?? 0)}
      </p>

      <div className="qr">
        <canvas ref={canvas} />
      </div>

      <p className="muted">
        Berlaku {mins}:{secs}
      </p>

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
