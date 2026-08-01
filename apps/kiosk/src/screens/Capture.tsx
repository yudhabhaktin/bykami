import { useCallback, useEffect, useRef, useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError } from "../api";
import { record, timed, type Timings } from "../perf";
import { cellAspect } from "../shot";
import { recall, remember } from "../stash";

/**
 * The countdown before the first shot.
 *
 * Longer than the ones that follow, because this is the only one a customer
 * spends walking from the screen back into frame. After that they are already
 * standing there and five seconds is a queue forming behind them.
 */
const FIRST_COUNTDOWN = 5;

/** Between shots, once they are in position. The photobooth 3–2–1. */
const NEXT_COUNTDOWN = 3;

/**
 * How long the shot just taken stays on screen before the next countdown.
 *
 * Long enough to see what you did and react to it — the pose change between
 * frames is most of what makes a strip worth keeping — and short enough that a
 * four-frame strip is still under a minute.
 */
const HOLD_MS = 1400;

/** JPEG quality for a browser-captured frame. */
const WEBCAM_QUALITY = 0.92;

/**
 * How long the camera may take to open before the booth calls it broken.
 *
 * getUserMedia does not always fail when there is nothing to open: with no
 * device attached, or a permission prompt nobody answered, the promise simply
 * never settles. Without this the screen says "menyiapkan kamera…" until
 * somebody power-cycles the PC, which is the exact failure the "failed" state
 * was added to make visible.
 *
 * Fifteen seconds is well past a slow cold start on a booth PC and well short
 * of a customer walking away.
 */
const CAMERA_TIMEOUT_MS = 15_000;

type Phase = "idle" | "counting" | "shooting" | "holding" | "done";

/**
 * The deadline for this session's photo time, as a millisecond timestamp.
 *
 * Anchored the first time the camera screen opens and then remembered, so a
 * reload does not hand out another five minutes. It is the kiosk that keeps
 * this clock and not the agent, deliberately: the server refusing a frame on
 * time would cut somebody off mid-pose with their money already taken, whereas
 * a screen that has run out still lets them keep every frame they have and
 * choose from them.
 */
function deadlineFor(sessionID: string, minutes: number): number {
  const held = recall(sessionID, "deadline");
  if (held) {
    const at = Number(held);
    if (Number.isFinite(at)) return at;
  }
  const at = Date.now() + minutes * 60_000;
  remember(sessionID, "deadline", String(at));
  return at;
}

export function Capture({
  state,
  refresh,
  setStep,
  setError,
  onTimings,
  templateId,
  minutes,
}: ScreenProps & { templateId: string; minutes: number }) {
  const video = useRef<HTMLVideoElement>(null);
  const stream = useRef<MediaStream | null>(null);
  const [count, setCount] = useState(0);
  const [phase, setPhase] = useState<Phase>("idle");
  const [flash, setFlash] = useState(false);
  // What the run in progress is shooting towards. Fixed when the run starts
  // rather than recomputed while it goes: takes climbs as frames land, and a
  // target derived from it would move away from the customer as they posed.
  const [target, setTarget] = useState(0);
  // The camera-preparation phase. Opening a webcam on a booth PC is the longest
  // single wait in the flow and it is the driver's, not ours — so it gets a
  // screen that says what is happening instead of a dead button. "failed" is
  // distinct from "opening" because a booth that says "preparing…" forever
  // looks like a slow machine rather than a broken one, and nobody calls staff.
  const [camera, setCamera] = useState<"opening" | "live" | "failed">("opening");

  // Set when the customer stops a run. A ref rather than state because the
  // sequence is a running async function, and it has to see the change on its
  // next tick rather than on the next render.
  const stopped = useRef(false);

  const session = state.session;
  const takes = session?.takes ?? 0;
  const limit = session?.take_limit ?? 0;
  const atLimit = takes >= limit;
  const webcam = state.source === "webcam";

  // Seconds of photo time left. Held in state so the screen can show it, but
  // derived from a fixed deadline rather than counted down from five minutes —
  // a tab the browser throttled in the background would otherwise finish with
  // time on the clock.
  const deadline = useRef(0);
  if (deadline.current === 0 && session) {
    deadline.current = deadlineFor(session.id, minutes);
  }
  const [left, setLeft] = useState(() =>
    Math.max(0, Math.ceil((deadline.current - Date.now()) / 1000)),
  );
  const expired = left === 0;

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

    const giveUp = setTimeout(() => {
      if (cancelled) return;
      setCamera("failed");
      setError("Kamera tidak merespons. Panggil petugas.");
    }, CAMERA_TIMEOUT_MS);

    void navigator.mediaDevices
      .getUserMedia({ video: { width: { ideal: 1920 }, height: { ideal: 1080 } }, audio: false })
      .then((s) => {
        clearTimeout(giveUp);
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
        clearTimeout(giveUp);
        if (cancelled) return;
        setCamera("failed");
        setError("Kamera tidak bisa diakses. Panggil petugas.");
      });

    return () => {
      cancelled = true;
      stopped.current = true;
      clearTimeout(giveUp);
      stream.current?.getTracks().forEach((t) => t.stop());
      stream.current = null;
      setCamera("opening");
    };
  }, [webcam, setError]);

  // Nothing to warm up on the tethered path: the camera's own software has the
  // sensor and the frame arrives through the hot folder.
  const preparing = webcam && camera === "opening";
  const broken = webcam && camera === "failed";

  useEffect(() => {
    const tick = () =>
      setLeft(Math.max(0, Math.ceil((deadline.current - Date.now()) / 1000)));
    tick();
    const t = setInterval(tick, 1000);
    return () => clearInterval(t);
  }, []);

  // Time up. A sequence already running is halted through the same flag
  // "Berhenti" sets, so the loop unwinds on its next tick rather than firing
  // one more frame into a session that has ended.
  //
  // Frames already taken are kept and the customer goes on to choose from them:
  // they paid for this, and a booth that expires into an empty screen has taken
  // money and given nothing back. With nothing shot at all there is nowhere to
  // send them, so the screen says the time is up and waits.
  useEffect(() => {
    if (!expired) return;
    stopped.current = true;
    if (takes > 0) setStep("review");
  }, [expired, takes, setStep]);

  /** Fires the shutter once. Reports whether a frame was actually captured. */
  const shoot = useCallback(async (): Promise<boolean> => {
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
      return true;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal mengambil foto.");
      return false;
    }
  }, [webcam, refresh, setError, onTimings]);

  /**
   * The whole strip, on its own.
   *
   * A photobox does not ask for a tap per frame — you press start, it counts
   * down, and it keeps going until the strip is full. Tapping between shots
   * means somebody has to be within reach of the screen, which is the opposite
   * of standing where the camera can see you.
   *
   * It stops on the first failure rather than carrying on. Firing three more
   * times into a camera that has just refused produces three more errors and a
   * customer watching a booth argue with itself.
   */
  const run = useCallback(async () => {
    stopped.current = false;
    setError("");

    // The strip is the target; the session's take limit is the ceiling. A
    // session resumed halfway counts what it already has, so a run after
    // "Foto lagi" tops the strip up rather than starting from nothing — and a
    // run after "Ulangi" shoots another strip's worth on top.
    const target = stripTarget(takes, need, limit);
    setTarget(target);
    let done = takes;

    for (let shot = 0; done < target; shot++) {
      setPhase("counting");
      for (let c = shot === 0 ? FIRST_COUNTDOWN : NEXT_COUNTDOWN; c > 0; c--) {
        setCount(c);
        if (await pause(1000, stopped)) return setPhase("idle");
      }
      setCount(0);

      setPhase("shooting");
      if (!(await shoot())) return setPhase("idle");
      done++;
      if (stopped.current) return setPhase("idle");

      if (done < target) {
        setPhase("holding");
        if (await pause(HOLD_MS, stopped)) return setPhase("idle");
      }
    }

    // Stops here rather than going straight on to choosing. The strip is full,
    // and the moment a customer knows whether they liked it is now, while they
    // are still standing in front of the camera — "Ulangi" from the review
    // screen is a walk back into frame after they have already sat down.
    setPhase("done");
  }, [need, limit, takes, shoot, setError]);

  const running = phase !== "idle" && phase !== "done";
  // The tethered path has no shutter to drive, so the sequence is a webcam
  // feature and that path keeps the one-frame-at-a-time button.
  const auto = webcam;

  return (
    <div className="grow" style={{ display: "flex", flexDirection: "column", gap: "1rem" }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline" }}>
        <h2>
          {session?.package_name}
          {template && <span className="muted small"> · {template.name}</span>}
        </h2>
        <span className="counter">
          {/* Under a minute is where the number starts to matter, so that is
              where it turns red rather than from the start — a clock that has
              been urgent for five minutes is not urgent. */}
          <span className={left <= 60 ? "clock low" : "clock"}>{clock(left)}</span>
          {takes} / {need || limit} foto
        </span>
      </div>

      <div className="stage">
        {webcam ? (
          /*
            Masked to the frame's own cell shape, so the preview and the print
            agree. The video fills this box and the overflow is cropped exactly
            where compose.drawCover will crop it — what falls outside the box is
            what will not be printed, and the customer can see that while there
            is still time to move.
          */
          <div className="shot" style={{ aspectRatio: cellAspect(template) }}>
            <video ref={video} autoPlay playsInline muted />
          </div>
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
        {phase === "counting" && count > 0 && <div className="countdown">{count}</div>}
        {phase === "holding" && (
          <div className="shot-taken">
            <span>{takes} / {target}</span>
            <span className="small">Ganti gaya!</span>
          </div>
        )}
        {flash && <div className="flash" />}
      </div>

      {expired ? (
        <p className="notice">
          {takes === 0
            ? "Waktu sesi habis dan belum ada foto. Panggil petugas, ya."
            : "Waktu sesi habis. Lanjut pilih foto."}
        </p>
      ) : atLimit ? (
        <p className="notice">Sudah mencapai batas take. Lanjut pilih foto.</p>
      ) : (
        need > 0 && (
          <p className="notice">
            {running
              ? `Foto otomatis — ${need} kali, tidak usah pegang layar.`
              : phase === "done"
                ? "Sudah cukup! Ulangi kalau mau gaya lain, atau lanjut pilih foto."
                : enough
                  ? `Cukup untuk ${template?.name}. Ambil lagi kalau mau pilihan lebih banyak.`
                  : `Frame ini butuh ${need} foto — sudah ${takes}.`}
          </p>
        )
      )}

      <div className="actions">
        {running ? (
          // One way out, and it has to be reachable mid-sequence: a camera
          // pointed at somebody who has changed their mind is the case this
          // exists for.
          <button className="btn secondary big" onClick={() => { stopped.current = true; }}>
            Berhenti
          </button>
        ) : (
          <button
            className="btn big"
            onClick={() => void (auto ? run() : shoot())}
            disabled={atLimit || expired || preparing || broken}
          >
            {preparing
              ? "Menyiapkan…"
              : !auto
                ? "Ambil foto"
                : phase === "done"
                  ? "Ulangi foto"
                  : startLabel(takes, need)}
          </button>
        )}
        <button
          className="btn secondary big"
          onClick={() => setStep("review")}
          disabled={takes === 0 || running}
        >
          Pilih foto ({takes})
        </button>
      </div>
    </div>
  );
}

/** "4:32" — the photo session's clock, as a customer reads a timer. */
function clock(seconds: number): string {
  const mins = Math.floor(seconds / 60);
  return `${mins}:${String(seconds % 60).padStart(2, "0")}`;
}

/**
 * How many frames this run is trying to reach.
 *
 * Enough to fill the strip, and a strip's worth more when it is already full —
 * which is what "Ulangi" asks for. Without that second case a retake computes a
 * target it has already met and the sequence ends before the countdown starts,
 * which looks exactly like a dead button.
 *
 * Capped by the take limit either way: that is what was paid for.
 */
function stripTarget(takes: number, need: number, limit: number): number {
  return Math.min(takes >= need ? takes + need : need, limit);
}

function startLabel(takes: number, need: number): string {
  if (takes === 0) return "Mulai sesi foto";
  return takes < need ? `Lanjut (${need - takes} lagi)` : "Ambil lagi";
}

/**
 * Waits, and reports whether it was cut short.
 *
 * Polled in slices rather than one long timer so that "Berhenti" during a
 * 5-second countdown stops within a tick instead of at the end of it.
 */
function pause(ms: number, stopped: { current: boolean }): Promise<boolean> {
  const step = 100;
  return new Promise((resolve) => {
    let left = ms;
    const id = setInterval(() => {
      if (stopped.current) {
        clearInterval(id);
        resolve(true);
        return;
      }
      left -= step;
      if (left <= 0) {
        clearInterval(id);
        resolve(false);
      }
    }, step);
  });
}

/**
 * grabFrame reads the current video frame as a JPEG.
 *
 * Deliberately not mirrored, although the preview is: a mirrored preview stops
 * people leaning the wrong way, and a mirrored print has the text on their
 * shirt backwards.
 *
 * Also deliberately unfiltered. The filter is applied when the sheet is
 * composed, so the original on disk is the frame the camera saw and a customer
 * who changes their mind at review has not lost anything.
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
