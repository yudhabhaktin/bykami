import { useCallback, useEffect, useRef, useState } from "react";

import type { ScreenProps } from "../App";
import { api, ApiError, previews } from "../api";
import { openCamera } from "../camera";
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
 * How many seconds of camera each photo keeps behind it.
 *
 * Five, which is the first countdown exactly — but the buffer rolls rather than
 * starting with each countdown, so every shot gets the same five seconds and
 * not three for the ones with the shorter count. What that buys on the later
 * shots is the tail of the one before: people reacting to the frame they just
 * took, which is the best footage in the session and is over before anybody
 * thinks to pose for it.
 */
const CLIP_SECONDS = 5;

/**
 * Frames a second in the clip.
 *
 * Matches clip.FPS in the agent, which encodes the GIF. It must divide 100:
 * GIF measures delay in hundredths of a second, and a rate that does not divide
 * cleanly plays at a length nobody chose.
 */
const CLIP_FPS = 10;

/**
 * The long edge of a clip frame, and it matches clip.LongEdge in the agent.
 *
 * Grabbed at the size it will be delivered at, so the agent has nothing to
 * rescale — this is fifty frames per shot travelling to a phone over mobile
 * data, and every pixel is paid for fifty times. Sending 1080p here would cost
 * the booth PC the encode, the network the upload, and the customer nothing
 * they could see in a 256-colour animation.
 */
const CLIP_LONG_EDGE = 400;

/** Quality for a clip frame. Lower than a photograph's: it is about to be
 *  quantised to 256 colours and dithered, which hides far more than this does. */
const CLIP_QUALITY = 0.7;

const CLIP_FRAMES = CLIP_SECONDS * CLIP_FPS;

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

  // The last CLIP_SECONDS of camera, oldest first — a ring buffer that runs for
  // as long as a strip is being shot. Refs rather than state throughout: it
  // changes ten times a second and nothing on screen draws from it, so putting
  // it in state would re-render the countdown at 10 Hz for no reason.
  const moment = useRef<Blob[]>([]);
  const rolling = useRef<number | null>(null);
  // Skips a tick rather than queueing behind a slow encode. On a tired booth PC
  // an unguarded interval piles up grabs faster than the canvas retires them.
  const grabbing = useRef(false);

  const session = state.session;
  const takes = session?.takes ?? 0;
  const limit = session?.take_limit ?? 0;
  const atLimit = takes >= limit;

  // Two different questions, and hybrid is why they had to stop being one. It
  // shows a live camera *and* takes its frames from the hot folder: the preview
  // is there so the customer can see themselves pose, while the picture that
  // gets printed comes off the tethered camera at full resolution.
  const preview = previews(state.source);
  const posts = state.source === "webcam";
  const cameraHint = state.camera;

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

  // The browser owns the camera on both previewing paths. On a bare hot folder
  // there is nothing to show here: the camera's own software has the sensor,
  // and the frame arrives through the hot folder afterwards.
  useEffect(() => {
    if (!preview) return;
    let cancelled = false;
    const openedAt = performance.now();

    const giveUp = setTimeout(() => {
      if (cancelled) return;
      setCamera("failed");
      setError("Kamera tidak merespons. Panggil petugas.");
    }, CAMERA_TIMEOUT_MS);

    // Wrapped in an async call rather than left as a bare promise chain: on an
    // insecure origin `navigator.mediaDevices` is undefined, and reaching for
    // it throws *synchronously* — out of the effect entirely, past any .catch()
    // on the chain it was supposed to start, taking the whole app down to a
    // blank screen instead of showing the message below.
    void (async () => {
      try {
        const s = await openCamera(cameraHint);
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
      } catch (err) {
        clearTimeout(giveUp);
        if (cancelled) return;
        setCamera("failed");
        setError("Kamera tidak bisa diakses. Panggil petugas.");
        // The customer gets one message for every camera failure; the reason
        // they differ belongs to whoever reads the booth's console.
        console.error("camera", err);
      }
    })();

    return () => {
      cancelled = true;
      stopped.current = true;
      clearTimeout(giveUp);
      stream.current?.getTracks().forEach((t) => t.stop());
      stream.current = null;
      setCamera("opening");
    };
  }, [preview, cameraHint, setError]);

  // Nothing to warm up on the tethered path: the camera's own software has the
  // sensor and the frame arrives through the hot folder.
  const preparing = preview && camera === "opening";
  const broken = preview && camera === "failed";

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

  /**
   * Starts keeping the last few seconds of camera.
   *
   * Runs for the length of a strip rather than the length of the session: a
   * booth waiting at the "Mulai" button has nothing worth remembering, and an
   * interval encoding JPEGs at 10 Hz for five idle minutes is a booth PC's fan
   * for no reason.
   */
  const startRolling = useCallback(() => {
    // Tied to the posting path, not the previewing one. A clip has to be filed
    // against the photo it belongs to, and the tethered capture hands back no
    // photo id — the frame has not been taken yet, let alone ingested. So a
    // hybrid booth shows a live preview and keeps no motion from it.
    if (!posts || rolling.current !== null) return;

    moment.current = [];
    rolling.current = window.setInterval(() => {
      if (grabbing.current) return;
      grabbing.current = true;

      grabFrame(video.current, CLIP_LONG_EDGE, CLIP_QUALITY)
        .then((f) => {
          moment.current.push(f);
          if (moment.current.length > CLIP_FRAMES) moment.current.shift();
        })
        // A dropped frame is a hundredth of a second missing from a clip that
        // is a bonus on top of the photograph. It must never reach the screen
        // as an error, and it must never stop the strip being shot.
        .catch(() => {})
        .finally(() => {
          grabbing.current = false;
        });
    }, 1000 / CLIP_FPS);
  }, [posts]);

  const stopRolling = useCallback(() => {
    if (rolling.current === null) return;
    clearInterval(rolling.current);
    rolling.current = null;
    moment.current = [];
  }, []);

  // An interval outlives the component that started it. Without this, walking
  // off the camera screen mid-run leaves the webcam being encoded forever.
  useEffect(() => stopRolling, [stopRolling]);

  /** Fires the shutter once. Reports whether a frame was actually captured. */
  const shoot = useCallback(async (): Promise<boolean> => {
    setFlash(true);
    setTimeout(() => setFlash(false), 320);

    const t: Timings = {};
    const firedAt = performance.now();

    try {
      if (posts) {
        const frame = await timed(t, "encode", () => grabFrame(video.current));

        // Taken now, not after the upload. The buffer keeps filling while the
        // frame is in flight, so a snapshot read later is five seconds that
        // begin after the shutter rather than end at it.
        const clip = moment.current.slice();

        record(t, "bytes", frame.size);
        const { photo } = await timed(t, "upload", () => api.capture(frame));
        sendClip(photo.id, clip);
      } else {
        // The tethered path: no pixels to send, because the frame is coming
        // down the USB cable into the hot folder. The agent either fires the
        // camera itself or announces the moment for somebody to fire it by
        // hand, and either way the answer is checked — an unchecked request
        // here would let the run count down over a camera that refused.
        await timed(t, "upload", api.fire);
      }
      await timed(t, "refresh", refresh);
      record(t, "shutter", performance.now() - firedAt);
      onTimings(t);
      return true;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Gagal mengambil foto.");
      return false;
    }
  }, [posts, refresh, setError, onTimings]);

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

    // Started with the run, so the first countdown fills the buffer and the
    // first shot has its five seconds by the time the shutter fires. Stopped in
    // the finally below, which is what every early return here goes through.
    startRolling();

    try {
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

      // Stops here rather than going straight on to choosing. The strip is
      // full, and the moment a customer knows whether they liked it is now,
      // while they are still standing in front of the camera — "Ulangi" from
      // the review screen is a walk back into frame after they have already sat
      // down.
      setPhase("done");
    } finally {
      stopRolling();
    }
  }, [need, limit, takes, shoot, setError, startRolling, stopRolling]);

  const running = phase !== "idle" && phase !== "done";
  // The automatic 3-2-1 needs something to fire at the end of it. The browser's
  // own camera always counts; a tethered one counts once the agent has a
  // shutter wired up. Without either, the countdown would end with nobody
  // photographed, so that booth keeps the one-frame-at-a-time button and
  // somebody presses the camera by hand.
  const auto = posts || state.shutter;

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
        {preview ? (
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
 *
 * longEdge scales the grab down on the way out, which is how a clip frame costs
 * a fraction of a photograph. Zero means the sensor's own size, which is what
 * the printed frame needs and the only thing that should ever ask for it.
 */
async function grabFrame(
  el: HTMLVideoElement | null,
  longEdge = 0,
  quality = WEBCAM_QUALITY,
): Promise<Blob> {
  if (!el || !el.videoWidth) throw new Error("kamera belum siap");

  let { videoWidth: w, videoHeight: h } = el;
  if (longEdge > 0 && Math.max(w, h) > longEdge) {
    const scale = longEdge / Math.max(w, h);
    // Rounded up off zero: a dimension of zero is a canvas that throws.
    w = Math.max(1, Math.round(w * scale));
    h = Math.max(1, Math.round(h * scale));
  }

  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;

  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("canvas tidak tersedia");
  ctx.drawImage(el, 0, 0, w, h);

  return new Promise((resolve, reject) => {
    canvas.toBlob(
      (blob) => (blob ? resolve(blob) : reject(new Error("gagal encode frame"))),
      "image/jpeg",
      quality,
    );
  });
}

/**
 * Sends a photo's seconds of motion, and does not wait for the answer.
 *
 * Fire-and-forget on purpose. This is twenty times the bytes of the frame it
 * belongs to, and the next countdown starts immediately — awaiting it would put
 * a customer's pose behind an upload nobody is waiting for. Nothing downstream
 * needs the result: the agent renders the animation on its own schedule, and
 * the download page shows whichever ones are ready.
 *
 * Failures are swallowed for the same reason they are on the frame sync: a clip
 * is a bonus on top of the photographs, and no part of it may ever put an error
 * in front of a paying customer or interrupt a strip being shot.
 */
function sendClip(photoId: string, frames: Blob[]): void {
  if (frames.length < 2) return;
  void api.clip(photoId, frames).catch(() => {});
}
