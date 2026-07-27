import { useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError, type Photo } from "../api";

/**
 * The phone number, captured at the moment of peak delight.
 *
 * The print is unconditional — they paid, they get it — and the digital files
 * need a number. There is no OTP here: the number is stored unverified and
 * earns no loyalty until it is verified through the cloud's OTP flow, which is
 * what keeps the append-only ledger clean given the number *is* the account.
 *
 * Two purposes, therefore two consents. Bundling them is the most common PDP
 * mistake, and neither box is pre-ticked because a pre-ticked box is not
 * consent. Both rules are enforced on the server too.
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

  async function submit() {
    setBusy(true);
    setError("");
    try {
      await api.delivery(phone, consent, marketing);
      await api.close();
      setStep("done");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal menyimpan nomor.");
      setBusy(false);
    }
  }

  async function skip() {
    // Declining is a real choice, not a dead end. The print already came out.
    setBusy(true);
    await api.close().catch(() => undefined);
    setStep("done");
  }

  return (
    <div className="grow" style={{ display: "flex", flexDirection: "column", gap: "1.25rem" }}>
      <h1>Ambil file fotomu</h1>
      <p className="muted">
        Masukkan nomor WhatsApp untuk menerima link download {selected.length} foto.
      </p>

      <div className="card" style={{ display: "grid", gap: "0.5rem" }}>
        <label htmlFor="phone">Nomor WhatsApp</label>
        <input
          id="phone"
          type="tel"
          inputMode="numeric"
          autoComplete="off"
          placeholder="0812…"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
        />

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

        <ul className="terms muted small">
          <li>
            Foto tersimpan <strong>{state.consent.retention_days} hari</strong>, setelah itu
            terhapus otomatis.
          </li>
          <li>Siapa pun yang punya link bisa membuka galeri — bagikan seperlunya.</li>
          <li>Di bawah 18 tahun? Minta pendampingan orang tua atau wali.</li>
        </ul>
      </div>

      <div className="actions">
        <button className="btn ghost" onClick={() => void skip()} disabled={busy}>
          Lewati
        </button>
        <button
          className="btn"
          onClick={() => void submit()}
          disabled={busy || !consent || phone.trim() === ""}
        >
          Kirim link
        </button>
      </div>
    </div>
  );
}
