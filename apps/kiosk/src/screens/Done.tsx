import { useEffect } from "react";

import type { ScreenProps } from "../App";

/** How long the thank-you stays up before the booth resets itself. */
const RESET_MS = 12_000;

/**
 * The last screen, and the one that has to reset itself.
 *
 * A customer walks away when they have their prints. Nobody taps "finish", so
 * a booth that waits for one is a booth showing the previous customer's screen
 * to the next.
 */
export function Done({ refresh, setStep }: ScreenProps) {
  useEffect(() => {
    const t = setTimeout(() => {
      void refresh();
      setStep("idle");
    }, RESET_MS);
    return () => clearTimeout(t);
  }, [refresh, setStep]);

  return (
    <div className="grow center">
      <h1>Makasih sudah mampir!</h1>
      <p className="muted">Ambil hasil cetakmu di bawah layar.</p>
      <p className="muted small">#SobatKAMi</p>
      <button
        className="btn secondary"
        onClick={() => {
          void refresh();
          setStep("idle");
        }}
      >
        Selesai
      </button>
    </div>
  );
}
