import { useEffect } from "react";

import type { ScreenProps } from "../App";
import { Doodle } from "../Doodle";

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
      setStep("attract");
    }, RESET_MS);
    return () => clearTimeout(t);
  }, [refresh, setStep]);

  return (
    <div className="grow center">
      <Doodle shape="bloom" className="done-doodle" />
      <h1>
        <span className="hand">Makasih</span> sudah mampir!
      </h1>
      <p>Ambil hasil cetakmu di bawah layar.</p>
      <p className="muted small">#SobatKAMi</p>
      <button
        className="btn secondary"
        onClick={() => {
          void refresh();
          setStep("attract");
        }}
      >
        Selesai
      </button>
    </div>
  );
}
