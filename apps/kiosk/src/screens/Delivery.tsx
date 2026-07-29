import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";

import type { ScreenProps } from "../App";
import { api, ApiError, type Photo } from "../api";
import { Doodle } from "../Doodle";

/**
 * Taking the files home.
 *
 * The booth's input device is a touchscreen, which makes typing the single most
 * expensive thing this screen can ask for. It used to open with a phone-number
 * field and a disabled button — eleven digits on an on-screen keyboard, with a
 * queue behind you, to receive photographs you are standing next to.
 *
 * So the QR is the delivery and it costs nothing: the customer already has a
 * camera out, and scanning it opens their own photos on their own phone. The
 * number is now one optional tap away for anyone who wants the link sent to
 * WhatsApp as well, and nothing on this screen is blocked on giving it.
 *
 * The print is unconditional and already in their hand. Every path from here,
 * including walking away, leaves them with what they paid for.
 */
export function Delivery({
  state,
  setStep,
  setError,
  selected,
}: ScreenProps & { selected: Photo[] }) {
  const [phone, setPhone] = useState("");
  const [consent, setConsent] = useState(false);
  const [marketing, setMarketing] = useState(false);
  const [busy, setBusy] = useState(false);
  // Whether the WhatsApp form is showing. Closed by default — that is the whole
  // point of this screen's redesign — and it is the only thing here that raises
  // a keyboard.
  const [wa, setWa] = useState(false);
  const [saved, setSaved] = useState(false);

  const canvas = useRef<HTMLCanvasElement>(null);
  const url = state.session?.share_url ?? "";
  const days = state.consent.retention_days;

  // Drawn locally from the URL the agent handed over, exactly as the payment QR
  // is drawn from the gateway's payload. Nothing is fetched: the booth has to
  // work with the network down, and the page this points at is served by the
  // booth itself.
  useEffect(() => {
    if (!canvas.current || !url) return;
    void QRCode.toCanvas(canvas.current, url, {
      errorCorrectionLevel: "M",
      margin: 1,
      width: 512,
    }).catch(() => setError("Gagal menampilkan QR. Panggil petugas."));
  }, [url, setError]);

  /** Stores the number for WhatsApp delivery. Never a precondition for leaving. */
  async function saveNumber() {
    setBusy(true);
    setError("");
    try {
      await api.delivery(phone, consent, marketing);
      setSaved(true);
      setWa(false);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal menyimpan nomor.");
    } finally {
      setBusy(false);
    }
  }

  /** Ends the session. The only button that has to be pressed. */
  async function finish() {
    setBusy(true);
    await api.close().catch(() => undefined);
    setStep("done");
  }

  return (
    <div className="delivery">
      <div className="delivery-head">
        <Doodle shape="burst" className="delivery-doodle" />
        <h1>
          Fotomu <span className="hand">siap!</span>
        </h1>
        <p className="muted">
          {selected.length} foto, tinggal dibawa pulang. Cetakannya sudah keluar di bawah layar.
        </p>
      </div>

      {url ? (
        <div className="pickup">
          <div className="qr">
            <canvas ref={canvas} />
          </div>
          <div className="pickup-copy">
            <h2>Scan buat simpan</h2>
            <p>Arahkan kamera HP ke kode ini. Nggak perlu ketik apa-apa.</p>
            <ul className="terms muted small">
              <li>
                Foto tersimpan <strong>{days} hari</strong>, setelah itu terhapus otomatis.
              </li>
              <li>Siapa pun yang punya link bisa membukanya — bagikan seperlunya.</li>
            </ul>
          </div>
        </div>
      ) : (
        /*
          No public hostname, so there is no page for a QR to point at. Said
          plainly rather than shown as an empty box: the print is still theirs,
          and the number is still worth leaving.
        */
        <p className="notice">
          Booth ini belum bisa kirim file digital. Cetakanmu sudah keluar di bawah layar — kalau
          mau filenya, tinggalkan nomor WhatsApp di bawah.
        </p>
      )}

      {saved && (
        <p className="notice">
          <strong>Nomor tersimpan.</strong> Link-nya kami kirim ke WhatsApp kamu.
          {url && " Mau sekarang? Scan QR di atas."}
        </p>
      )}

      {wa ? (
        <div className="card wa-form">
          <label htmlFor="phone">Nomor WhatsApp</label>
          <input
            id="phone"
            type="tel"
            inputMode="numeric"
            autoComplete="off"
            placeholder="0812…"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            autoFocus
          />

          {/*
            Two purposes, therefore two consents, and neither is pre-ticked —
            a pre-ticked box is not consent. Both rules are enforced on the
            server too, so the disabled button below is a courtesy rather than
            the control.
          */}
          <label className="consent">
            <input
              type="checkbox"
              checked={consent}
              onChange={(e) => setConsent(e.target.checked)}
            />
            <span>
              Saya setuju foto dan nomor saya diproses untuk mengirim file ini.{" "}
              <strong>(wajib)</strong>
            </span>
          </label>

          <label className="consent">
            <input
              type="checkbox"
              checked={marketing}
              onChange={(e) => setMarketing(e.target.checked)}
            />
            <span>Boleh kirimi aku info promo KAMi. Bisa berhenti kapan saja. (opsional)</span>
          </label>

          <p className="muted small">Di bawah 18 tahun? Minta pendampingan orang tua atau wali.</p>

          <div className="actions">
            <button className="btn ghost" onClick={() => setWa(false)} disabled={busy}>
              Batal
            </button>
            <button
              className="btn"
              onClick={() => void saveNumber()}
              disabled={busy || !consent || phone.trim() === ""}
            >
              Simpan nomor
            </button>
          </div>
        </div>
      ) : (
        !saved && (
          <button className="btn secondary wa-open" onClick={() => setWa(true)} disabled={busy}>
            <span aria-hidden="true">💬</span> Kirim ke WhatsApp juga
          </button>
        )
      )}

      <div className="actions delivery-actions">
        <button className="btn big" onClick={() => void finish()} disabled={busy}>
          Selesai
        </button>
      </div>
    </div>
  );
}
