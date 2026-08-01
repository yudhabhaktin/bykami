import type { ScreenProps } from "../App";
import { Doodle } from "../Doodle";

/**
 * What was bought, and the one thing about it the customer still decides.
 *
 * It sits between paying and picking a frame because cut is a property of the
 * paper, not of the design: the same strip artwork comes out either as two 2x6
 * strips the printer's blade has separated, or as one 4x6 kept whole. Asking
 * after the frame would put a question about the print between choosing a look
 * and standing in front of the camera, which is the wrong order to think in.
 *
 * It is also the only screen that states the session's shape — the minutes, the
 * takes, the one included print — and stating it once, here, is what stops the
 * capture screen and the review screen each having to explain themselves.
 */
export function Session({
  state,
  setStep,
  cut,
  setCut,
  minutes,
}: ScreenProps & {
  cut: boolean;
  setCut: (cut: boolean) => void;
  minutes: number;
}) {
  const takes = state.session?.take_limit ?? 0;

  return (
    <div className="grow">
      <div className="page-head">
        <Doodle shape="camera" className="page-doodle ink" />
        <h1>Single Session</h1>
        <p className="muted">
          {minutes} menit di depan kamera · maksimal {takes}x take · 1 cetak
        </p>
      </div>

      <div className="page-head">
        <h2>Hasil cetak</h2>
        <p className="muted">Mau dipotong jadi dua strip, atau utuh satu lembar?</p>
      </div>

      <div className="cut-options">
        <button
          className="cut-option"
          aria-pressed={cut}
          onClick={() => setCut(true)}
        >
          <Doodle shape="rainbow" className="cut-art green" />
          <span className="cut-name">Cut</span>
          <span className="muted small">
            Dipotong jadi 2 strip 2x6 — satu buat kamu, satu buat dibagi.
          </span>
        </button>

        <button
          className="cut-option"
          aria-pressed={!cut}
          onClick={() => setCut(false)}
        >
          <Doodle shape="bloom" className="cut-art ink" />
          <span className="cut-name">No Cut</span>
          <span className="muted small">
            Utuh satu lembar 4x6, tidak dipotong mesin.
          </span>
        </button>
      </div>

      <div className="actions">
        <button className="btn big" onClick={() => setStep("frame")}>
          Lanjut
        </button>
      </div>
    </div>
  );
}
