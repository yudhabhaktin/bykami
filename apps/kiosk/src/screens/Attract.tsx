import type { ScreenProps } from "../App";
import selfie from "../assets/selfie.svg";
import { Doodle } from "../Doodle";

/**
 * The screen a booth shows when nobody is standing in front of it.
 *
 * Separate from package selection because the two do different jobs: this one
 * has to be readable from across a room and invite a walk-up, and a price list
 * is neither. It is also the screen every timeout and every finished session
 * returns to, so it is the booth's resting state rather than a step in the flow.
 *
 * The whole panel is the button. A first-time customer should not have to find
 * a target, and on a kiosk there is nothing else on screen to hit by mistake.
 */
export function Attract({ state, setStep }: ScreenProps) {
  const from = Math.min(...state.packages.map((p) => p.price_idr));

  return (
    <button className="attract" onClick={() => setStep("packages")}>
      <Doodle shape="sprig" className="doodle doodle-tl" />
      <Doodle shape="bloom" className="doodle doodle-br" />

      <span className="attract-copy">
        <span className="attract-kicker">bykami · self photo studio</span>
        <span className="attract-title">
          Sentuh untuk <span className="hand">mulai</span>
        </span>
        <span className="attract-sub">
          Foto sepuasnya, pilih framemu, cetak langsung di tempat.
        </span>
        {Number.isFinite(from) && (
          <span className="attract-price">mulai {rupiahShort(from)}</span>
        )}
      </span>

      {/* Decorative. The alt text is empty on purpose — the heading beside it
          already says what the screen is for. */}
      <img className="attract-art" src={selfie} alt="" />
    </button>
  );
}

/** "Rp 45rb" — a price read at walking distance, not a receipt. */
function rupiahShort(n: number): string {
  return n >= 1000 ? `Rp ${Math.round(n / 1000)}rb` : `Rp ${n}`;
}
