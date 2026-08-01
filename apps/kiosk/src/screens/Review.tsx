import { useCallback, useEffect, useRef, useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError, rupiah, type Photo, type Template } from "../api";
import { filterCSS, FilterPicker } from "../Filters";
import { record, type Timings } from "../perf";
import { countdown, QRCanvas } from "../QRCanvas";
import { cellAspect } from "../shot";
import { SheetPreview } from "../SheetPreview";

/** How often the printer's progress is checked while the customer watches. */
const POLL_MS = 1500;

/** How often the reprint's QR code is checked for settlement. */
const PAY_POLL_MS = 2000;

/**
 * Pick the frames, print.
 *
 * The layout is no longer chosen here. It is settled two screens earlier,
 * before the camera opens, because the frame decides how many photographs the
 * session needs — and offering it again at review meant a customer could pick a
 * four-cell design after shooting three, which is a question asked far too
 * late. What is left here is the choice this screen is for: which frames go in,
 * and how they look.
 *
 * This is also the backstop the capture-side take limit does not replace: it
 * enforces what was actually paid for, because a stray file in the hot folder
 * must never become a free print.
 */
export function Review({
  state,
  refresh,
  setStep,
  setError,
  onTimings,
  onPrinted,
  templateId,
  filter,
  setFilter,
  cut,
}: ScreenProps & {
  onPrinted: (photos: Photo[]) => void;
  templateId: string;
  filter: string;
  setFilter: (id: string) => void;
  cut: boolean;
}) {
  const [photos, setPhotos] = useState<Photo[]>([]);
  const [chosen, setChosen] = useState<string[]>([]);
  const [job, setJob] = useState<{ id: string; state: string } | null>(null);
  const [busy, setBusy] = useState(false);

  // How long the whole filmstrip takes to decode and paint. The measurement the
  // derived-image worker exists to move: without a derivative the browser
  // decodes one full-resolution original per thumbnail.
  const paintedAt = useRef(0);
  const decoded = useRef(0);
  const measured = useRef(false);

  // The reprint's QR code, empty when nothing is being paid for. Held here
  // rather than in the shared state because it is a dialog over this screen:
  // the customer's selection and the sheet preview have to survive it.
  //
  // The countdown is separate state so that ticking it does not restart the
  // settlement poll — the two run at different rates and one must not reset the
  // other.
  const [payload, setPayload] = useState("");
  const [remaining, setRemaining] = useState(0);
  const [buying, setBuying] = useState(false);

  const load = useCallback(async () => {
    try {
      const { photos } = await api.photos(templateId);
      paintedAt.current = performance.now();
      decoded.current = 0;
      setPhotos(photos);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal memuat foto.");
    }
  }, [setError, templateId]);

  const onThumbLoaded = useCallback(
    (total: number) => {
      decoded.current++;
      if (decoded.current < total) return;
      // Only the first paint. A reload after switching template is served from
      // the browser cache and would overwrite the real measurement with a
      // near-zero one.
      if (measured.current) return;
      measured.current = true;

      const t: Timings = {};
      record(t, "filmstrip", performance.now() - paintedAt.current);
      onTimings(t);
    },
    [onTimings],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const template: Template | undefined = state.templates.find((t) => t.id === templateId);
  const need = template?.cells.length ?? 0;

  // The paid allowance, and what is left of it. Both come from the server, so
  // reloading the page mid-session cannot reset the count — and the allowance
  // itself only grows when a reprint payment settles.
  const printed = state.session?.prints_done ?? 0;
  const unclaimed = Math.max(0, (state.session?.print_copies ?? 1) - printed);

  // In tap order, which is cell order — the preview and the badge on each
  // thumbnail have to be showing the same thing.
  const chosenPhotos = chosen
    .map((id) => photos.find((p) => p.id === id))
    .filter((p): p is Photo => p !== undefined);

  function toggle(id: string) {
    setChosen((prev) => {
      if (prev.includes(id)) return prev.filter((x) => x !== id);
      if (prev.length >= need) return prev;
      // Order matters: it is the order the frames appear in the strip, so the
      // number badge on each thumbnail is its cell.
      return [...prev, id];
    });
  }

  // One at a time, not the whole allowance at once. The server counts what the
  // session has already claimed, so tapping again can never exceed what was
  // paid for — this is the screen asking politely for something the print route
  // enforces.
  const print = useCallback(async () => {
    if (!template || chosen.length !== need) return;
    setBusy(true);
    setError("");
    try {
      const { job } = await api.print(template.id, chosen, 1, filter, cut);
      setJob({ id: job.id, state: job.state });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal mencetak.");
      setBusy(false);
    }
  }, [template, chosen, need, filter, cut, setError]);

  // The session includes one sheet; everything after it is bought. The QR goes
  // up rather than the sheet coming out, and the allowance only moves when the
  // gateway says the money arrived.
  async function buyReprint() {
    setBuying(true);
    setError("");
    try {
      const { payment } = await api.reprint();
      setPayload(payment.qr_payload);
      setRemaining(payment.expires_in);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal membuat QR.");
    } finally {
      setBuying(false);
    }
  }

  // What to print once the money lands. A ref because the poll below must not
  // be restarted every time the customer taps a different thumbnail, and print
  // closes over the selection.
  const printNow = useRef(print);
  printNow.current = print;

  useEffect(() => {
    if (!payload) return;
    const t = setInterval(() => setRemaining((n) => Math.max(0, n - 1)), 1000);
    return () => clearInterval(t);
  }, [payload]);

  // Settlement is polled here for the same reason it is on the pay screen: the
  // booth has no inbound path a gateway webhook could reach.
  //
  // The sheet is queued the moment it is paid for, without a further tap. The
  // customer has just scanned a code to print this exact selection; asking them
  // to confirm it again is a tap that can only mean "yes".
  useEffect(() => {
    if (!payload) return;
    let cancelled = false;

    const t = setInterval(() => {
      void (async () => {
        try {
          const { payment } = await api.pollPayment();
          if (cancelled) return;
          setRemaining(payment.expires_in);

          if (payment.state === "settled") {
            setPayload("");
            await refresh();
            await printNow.current();
            return;
          }
          if (payment.state === "expired" || payment.state === "failed") {
            setPayload("");
            setError("Waktu pembayaran habis. Coba lagi kalau masih mau cetak.");
          }
        } catch {
          // A failed poll is not a failed payment. The customer is standing
          // right there, so the dialog stays put and asks again.
        }
      })();
    }, PAY_POLL_MS);

    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [payload, refresh, setError]);

  // The job's progress is polled because the agent owns the queue, which is the
  // whole reason it exists rather than window.print(): status, errors and media
  // remaining are things a browser cannot see.
  useEffect(() => {
    if (!job || job.state === "done" || job.state === "failed") return;

    const t = setInterval(() => {
      void api
        .printStatus(job.id)
        .then(({ job: next }) => {
          setJob({ id: next.id, state: next.state });
          if (next.state === "failed") {
            setError(next.error || "Cetak gagal. Panggil petugas.");
            setBusy(false);
          }
          if (next.state === "done") {
            // Back to this screen every time, never straight on to delivery.
            //
            // It used to move on as soon as the package's copies were used up,
            // which was right while the last included print was the end of the
            // transaction. It no longer is: the offer to buy another sheet
            // lives on this screen, so advancing past it would take the choice
            // away at the exact moment it becomes available. "Lanjut" is how a
            // customer says they are done, and it is one tap.
            void refresh().then(() => {
              setJob(null);
              setBusy(false);
            });
          }
        })
        .catch(() => undefined);
    }, POLL_MS);

    return () => clearInterval(t);
  }, [job, refresh, setError]);

  if (job && job.state !== "failed") {
    return (
      <div className="grow center">
        <h1>Sedang mencetak…</h1>
        <p className="muted">Ambil hasil cetak di bawah layar.</p>
      </div>
    );
  }

  return (
    <div className="review">
      <div className="preview">
        {template && <SheetPreview template={template} chosen={chosenPhotos} filter={filter} />}
      </div>

      <div className="picker">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
        <h2>Pilih {need} foto</h2>
        <span className="counter">
          {chosen.length} / {need}
        </span>
      </div>

      {/*
        Above the filmstrip: the filter changes the sheet in the preview beside
        it, so the choice is made against the thing being bought rather than
        against a name.
      */}
      <FilterPicker
        filters={state.filters}
        value={filter}
        onChange={setFilter}
        sample={photos[0] ? api.photoURL(photos[0].id) : undefined}
      />

      <div className="filmstrip">
        {photos.map((p) => {
          const index = chosen.indexOf(p.id);
          return (
            <button
              key={p.id}
              className="thumb"
              // The cell's shape, not the sensor's. The filmstrip is where the
              // customer decides which frame goes in the strip, and a 4:3
              // thumbnail of a photo bound for a tall cell shows them a
              // composition nobody will ever print.
              style={{ aspectRatio: cellAspect(template) }}
              aria-pressed={index >= 0}
              onClick={() => toggle(p.id)}
            >
              <img
                src={api.photoURL(p.id)}
                alt=""
                style={{ filter: filterCSS(filter) }}
                onLoad={() => onThumbLoaded(photos.length)}
              />
              {index >= 0 && <span className="order">{index + 1}</span>}
              {/*
                The resolution argument made visible. A frame that would print
                below 300 dpi says so on the screen rather than in a document —
                this is the difference between the DSLR and webcam rows of the
                table in design/kiosk.md.
              */}
              {p.print_dpi > 0 && p.print_dpi < 300 && (
                <span className="dpi">{p.print_dpi} dpi</span>
              )}
            </button>
          );
        })}
      </div>

      {printed > 0 && (
        <p className="notice">
          {unclaimed > 0
            ? `Cetakan ke-${printed} sudah keluar. Sisa ${unclaimed} cetak — ambil sekarang atau lanjut.`
            : `Cetakan ke-${printed} sudah keluar. Mau satu lagi? ${rupiah(state.reprint_idr)} per lembar.`}
        </p>
      )}

      <div className="actions">
        {printed === 0 ? (
          <button className="btn secondary" onClick={() => setStep("capture")} disabled={busy}>
            Foto lagi
          </button>
        ) : (
          // Declining another sheet has to be one tap, and it is the tap most
          // customers will take.
          <button
            className="btn secondary"
            onClick={() => onPrinted(chosenPhotos)}
            disabled={busy}
          >
            Lanjut
          </button>
        )}
        {unclaimed > 0 ? (
          <button
            className="btn"
            onClick={() => void print()}
            disabled={busy || chosen.length !== need}
          >
            {printed === 0 ? "Cetak" : `Cetak (${unclaimed} tersisa)`}
          </button>
        ) : (
          // Nothing left that was paid for, so the next sheet is a purchase.
          // Priced on the button: a customer should never tap something that
          // turns out to cost money.
          <button
            className="btn"
            onClick={() => void buyReprint()}
            disabled={busy || buying || chosen.length !== need}
          >
            Cetak 1 lagi · {rupiah(state.reprint_idr)}
          </button>
        )}
      </div>
      </div>

      {payload && (
        <div className="dialog" role="dialog" aria-label="Bayar cetakan tambahan">
          <div className="dialog-card">
            <h2>Scan untuk cetak 1 lagi</h2>
            <p className="muted">{rupiah(state.reprint_idr)} · 1 lembar</p>

            <QRCanvas payload={payload} size={360} onError={setError} />

            <p className="muted">Berlaku {countdown(remaining)}</p>

            <div className="actions">
              <button className="btn ghost" onClick={() => setPayload("")}>
                Batal
              </button>
              {/*
                Only rendered when the simulated provider is selected, exactly as
                on the pay screen: with a real provider the route it calls is a
                404, so a booth that could take real money has no button that
                skips paying.
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
        </div>
      )}
    </div>
  );
}
