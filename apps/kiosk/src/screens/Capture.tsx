import { useCallback, useEffect, useRef, useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError } from "../api";
import { record, timed, type Timings } from "../perf";

/**
 * The countdown before the shutter fires.
 *
 * Five seconds, because the tap has to be reachable from the posing position
 * or the countdown has to be long enough to walk back into frame — and which
 * of those is true depends on an input device that design/kiosk.md still lists
 * as undecided. Five is the usual answer and the one that survives either
 * choice.
 */
const COUNTDOWN = 5;

/** JPEG quality for a browser-captured frame. */
const WEBCAM_QUALITY = 0.92;

export function Capture({
  state,
  refresh,
  setStep,
  setError,
  onTimings,
  templateId,
}: ScreenProps & { templateId: string }) {
  const video = useRef<HTMLVideoElement>(null);
  const stream = useRef<MediaStream | null>(null);
  const [count, setCount] = useState<number | null>(null);
  const [flash, setFlash] = useState(false);
  const [busy, setBusy] = useState(false);
  // The camera-preparation phase. Opening a webcam on a booth PC is the longest
  // single wait in the flow and it is the driver's, not ours — so it gets a
  // screen that says what is happening instead of a dead button. "failed" is
  // distinct from "opening" because a booth that says "preparing…" forever
  // looks like a slow machine rather than a broken one, and nobody calls staff.
  const [camera, setCamera] = useState<"opening" | "live" | "failed">("opening");

  const session = state.session;
  const takes = session?.takes ?? 0;
  const limit = session?.take_limit ?? 0;
  const atLimit = takes >= limit;
  const webcam = state.source === "webcam";

  // Known before the shutter, which is the point of choosing the frame first:
  // "you need four" is useless advice after the fourth photo.
  const template = state.templates.find((t) => t.id === templateId);
  const need = template?.cells.length ?? 0;
  const enough = takes >= need;

  // The browser owns the camera on the webcam path. On the tethered path there
  // is nothing to preview here: the camera's own software has the sensor, and
  // the frame arrives through the hot folder afterwards.
  useEffect(() => {
    if (!webcam) return;
    let cancelled = false;
    const openedAt = performance.now();

    void navigator.mediaDevices
      .getUserMedia({ video: { width: { ideal: 1920 }, height: { ideal: 1080 } }, audio: false })
      .then((s) => {
        if (cancelled) {
          s.getTracks().forEach((t) => t.stop());
          return;
        }
        stream.current = s;
        if (video.current) video.current.srcObject = s;
        setCamera("live");

        // Cold-start cost of the camera itself, which on a booth PC is the
        // longest single wait in the whole flow and is entirely the driver's.
        const t: Timings = {};
        record(t, "camera", performance.now() - openedAt);
        onTimings(t);
      })
      .catch(() => {
        if (cancelled) return;
        setCamera("failed");
        setError("Kamera tidak bisa diakses. Panggil petugas.");
      });

    return () => {
      cancelled = true;
      stream.current?.getTracks().forEach((t) => t.stop());
      stream.current = null;
      setCamera("opening");
    };
  }, [webcam, setError]);

  // Nothing to warm up on the tethered path: the camera's own software has the
  // sensor and the frame arrives through the hot folder.
  const preparing = webcam && camera === "opening";
  const broken = webcam && camera === "failed";

  const shoot = useCallback(async () => {
    setFlash(true);
    setTimeout(() => setFlash(false), 320);

    const t: Timings = {};
    const firedAt = performance.now();

    try {
      if (webcam) {
        const frame = await timed(t, "encode", () => grabFrame(video.current));
        record(t, "bytes", frame.size);
        await timed(t, "upload", () => api.capture(frame));
      } else {
        // The tethered path. How a tap reaches a Canon's shutter is the last
        // open question in the capture design — a USB relay into the RS-60E3
        // jack is the recommendation — so until that hardware exists this
        // announces the moment and the frame is fired by hand.
        await fetch("/api/capture", { method: "POST" });
      }
      await timed(t, "refresh", refresh);
      record(t, "shutter", performance.now() - firedAt);
      onTimings(t);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal mengambil foto.");
    } finally {
      setBusy(false);
    }
  }, [webcam, refresh, setError, onTimings]);

  // The countdown auto-fires. A real photobooth 3–2–1 rather than an advisory
  // "get ready", which is what customers expect from the format and what the
  // app owning the shutter makes possible.
  useEffect(() => {
    if (count === null) return;
    if (count === 0) {
      setCount(null);
      void shoot();
      return;
    }
    const t = setTimeout(() => setCount(count - 1), 1000);
    return () => clearTimeout(t);
  }, [count, shoot]);

  function start() {
    if (busy || atLimit || preparing || count !== null) return;
    setBusy(true);
    setError("");
    setCount(COUNTDOWN);
  }

  return (
    <div className="grow" style={{ display: "flex", flexDirection: "column", gap: "1rem" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
        <h2>
          {session?.package_name}
          {template && <span className="muted small"> · {template.name}</span>}
        </h2>
        <span className="counter">
          {takes} / {limit} take
        </span>
      </div>

      <div className="stage">
        {webcam ? (
          <video ref={video} autoPlay playsInline muted />
        ) : (
          <p className="muted" style={{ color: "#fff", padding: "2rem", textAlign: "center" }}>
            Kamera tethering aktif. Foto akan muncul otomatis setelah diambil.
          </p>
        )}
        {(preparing || broken) && (
          <div className="preparing">
            <span>{broken ? "Kamera tidak terbaca" : "Menyiapkan kamera…"}</span>
            <span className="small">
              {broken ? "Panggil petugas, ya." : "Berdiri di depan layar, ya."}
            </span>
          </div>
        )}
        {count !== null && <div className="countdown">{count}</div>}
        {flash && <div className="flash" />}
      </div>

      {atLimit ? (
        <p className="notice">Sudah mencapai batas take. Lanjut pilih foto.</p>
      ) : (
        need > 0 && (
          <p className="notice">
            {enough
              ? `Cukup untuk ${template?.name}. Ambil lagi kalau mau pilihan lebih banyak.`
              : `Frame ini butuh ${need} foto — sudah ${takes}.`}
          </p>
        )
      )}

      <div className="actions">
        <button
          className="btn big"
          onClick={start}
          disabled={busy || atLimit || preparing || broken || count !== null}
        >
          {count !== null ? "Bersiap…" : preparing ? "Menyiapkan…" : "Ambil foto"}
        </button>
        <button
          className="btn secondary big"
          onClick={() => setStep("review")}
          disabled={takes === 0 || count !== null}
        >
          Pilih foto ({takes})
        </button>
      </div>
    </div>
  );
}

/**
 * grabFrame reads the current video frame as a JPEG.
 *
 * Deliberately not mirrored, although the preview is: a mirrored preview stops
 * people leaning the wrong way, and a mirrored print has the text on their
 * shirt backwards.
 */
async function grabFrame(el: HTMLVideoElement | null): Promise<Blob> {
  if (!el || !el.videoWidth) throw new Error("kamera belum siap");

  const canvas = document.createElement("canvas");
  canvas.width = el.videoWidth;
  canvas.height = el.videoHeight;

  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("canvas tidak tersedia");
  ctx.drawImage(el, 0, 0);

  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error("gagal encode frame"))),
      "image/jpeg",
      WEBCAM_QUALITY,
    );
  });
}
