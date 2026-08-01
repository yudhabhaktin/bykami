import { useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError } from "../api";
import selfie from "../assets/selfie.svg";
import { Doodle } from "../Doodle";

/**
 * The screen a booth shows when nobody is standing in front of it.
 *
 * It has to be readable from across a room and invite a walk-up. It is also the
 * screen every timeout and every finished session returns to, so it is the
 * booth's resting state rather than a step in the flow.
 *
 * The whole panel is the button. A first-time customer should not have to find
 * a target, and on a kiosk there is nothing else on screen to hit by mistake.
 *
 * The tap now opens the session outright, where it used to open a price list.
 * There is one session at one price, so a list of one is a screen that asks a
 * question with a single answer — and the QR code that follows states the price
 * anyway, in the place the customer is about to pay it.
 */
export function Attract({
  state,
  refresh,
  setStep,
  setError,
  setTemplateId,
}: ScreenProps & { setTemplateId: (id: string) => void }) {
  const [busy, setBusy] = useState(false);
  const price = state.packages[0]?.price_idr;

  async function start() {
    setBusy(true);
    setError("");
    try {
      const { session } = await api.start();
      // The session's own frame, preselected. The frame screen is a choice, not
      // a form to fill in, so tapping straight through has to land somewhere.
      setTemplateId(session.template_id);
      await refresh();
      setStep("pay");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal memulai sesi.");
      setBusy(false);
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
    <button className="attract" onClick={() => void start()} disabled={busy}>
      <Doodle shape="sprig" className="doodle doodle-tl" />
      <Doodle shape="bloom" className="doodle doodle-br" />

      <span className="attract-copy">
        <img
          className="attract-kicker"
          src="/logo.png"
          alt="studio by KAMI"
          width={640}
          height={210}
        />
        <span className="attract-title">
          Sentuh untuk <span className="hand">mulai</span>
        </span>
        <span className="attract-sub">
          Foto sepuasnya, pilih framemu, cetak langsung di tempat.
        </span>
        {price !== undefined && (
          <span className="attract-price">{rupiahShort(price)} sekali sesi</span>
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
