import { useCallback, useEffect, useRef, useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError, type Photo, type Template } from "../api";
import { record, type Timings } from "../perf";
import { SheetPreview } from "../SheetPreview";

/** How often the printer's progress is checked while the customer watches. */
const POLL_MS = 1500;

/**
 * Pick the frames, pick the design, print.
 *
 * This is also the backstop the capture-side take limit does not replace: it
 * enforces what was actually bought, because a stray file in the hot folder
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
  setTemplateId,
}: ScreenProps & {
  onPrinted: (photos: Photo[]) => void;
  templateId: string;
  setTemplateId: (id: string) => void;
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

  // Refetched when the template changes, because print_dpi is a property of the
  // cell a frame lands in and the customer can switch layout here.
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

  async function print() {
    if (!template || chosen.length !== need) return;
    setBusy(true);
    setError("");
    try {
      const { job } = await api.print(template.id, chosen, state.session?.print_copies ?? 1);
      setJob({ id: job.id, state: job.state });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal mencetak.");
      setBusy(false);
    }
  }

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
            void refresh();
            onPrinted(photos.filter((p) => chosen.includes(p.id)));
          }
        })
        .catch(() => undefined);
    }, POLL_MS);

    return () => clearInterval(t);
  }, [job, chosen, photos, refresh, onPrinted, setError]);

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
        {template && <SheetPreview template={template} chosen={chosenPhotos} />}
      </div>

      <div className="picker">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
        <h2>Pilih {need} foto</h2>
        <span className="counter">
          {chosen.length} / {need}
        </span>
      </div>

      <div className="actions">
        {state.templates.map((t) => (
          <button
            key={t.id}
            className="btn secondary"
            aria-pressed={t.id === templateId}
            onClick={() => {
              setTemplateId(t.id);
              setChosen([]);
            }}
            style={t.id === templateId ? { borderWidth: 2 } : undefined}
          >
            {t.name}
          </button>
        ))}
      </div>

      <div className="filmstrip">
        {photos.map((p) => {
          const index = chosen.indexOf(p.id);
          return (
            <button
              key={p.id}
              className="thumb"
              aria-pressed={index >= 0}
              onClick={() => toggle(p.id)}
            >
              <img src={api.photoURL(p.id)} alt="" onLoad={() => onThumbLoaded(photos.length)} />
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

      <div className="actions">
        <button className="btn secondary" onClick={() => setStep("capture")} disabled={busy}>
          Foto lagi
        </button>
        <button className="btn" onClick={() => void print()} disabled={busy || chosen.length !== need}>
          Cetak {state.session?.print_copies ?? 1}x
        </button>
      </div>
      </div>
    </div>
  );
}
