import { useCallback, useEffect, useState } from "react";

import { api, ApiError, type Photo, type State } from "./api";
import { initPerf, labels, perfEnabled, type Timings } from "./perf";
import { Attract } from "./screens/Attract";
import { Capture } from "./screens/Capture";
import { Delivery } from "./screens/Delivery";
import { Done } from "./screens/Done";
import { Frame } from "./screens/Frame";
import { Packages } from "./screens/Packages";
import { Pay } from "./screens/Pay";
import { Review } from "./screens/Review";

/*
 * One state machine, held here rather than in a router.
 *
 * The booth has no URLs. A customer cannot navigate, cannot go back, and must
 * never be able to reach a screen out of order — so the flow is a value, and
 * the server's session state is what it is derived from on every load. A
 * refresh, a browser restart or a power cut therefore resumes where the
 * customer was rather than dropping them at the start of a session they have
 * already paid for.
 *
 * "attract" is the resting state, not the first step: every timeout and every
 * finished session lands back there.
 */
export type Step =
  | "attract"
  | "packages"
  | "pay"
  | "frame"
  | "capture"
  | "review"
  | "delivery"
  | "done";

/** How long the price list may sit untouched before the booth goes back to attract. */
const WALKAWAY_MS = 45_000;

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [step, setStep] = useState<Step>("attract");
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Photo[]>([]);
  // Chosen on the frame screen and still changeable at review, so it outlives
  // both. Seeded from the package, which is the frame the price list sold.
  const [templateId, setTemplateId] = useState("");
  const [timings, setTimings] = useState<Timings>({});
  const [perf] = useState(initPerf);

  // Merged rather than replaced: the camera's cold start is measured once, on a
  // different screen from the shutter timings it should stay visible beside.
  const onTimings = useCallback((t: Timings) => {
    if (perfEnabled()) setTimings((prev) => ({ ...prev, ...t }));
  }, []);

  const refresh = useCallback(async () => {
    try {
      const next = await api.state();
      setState(next);
      return next;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Booth tidak merespons.");
      return null;
    }
  }, []);

  // On load, put the customer back where the server says they are.
  useEffect(() => {
    void (async () => {
      const next = await refresh();
      if (!next) return;
      if (!next.session) return setStep("attract");
      setTemplateId(next.session.template_id);
      if (next.session.state === "awaiting_payment") return setStep("pay");
      // Frames already taken means the frame screen has been through once;
      // sending them back to it would look like the booth forgot.
      setStep(next.session.takes > 0 ? "capture" : "frame");
    })();
  }, [refresh]);

  // A customer who walks away from the price list leaves the booth showing a
  // price list. Only before payment: a timeout that resets a paid session would
  // take money and give nothing back.
  useEffect(() => {
    if (step !== "packages") return;
    const t = setTimeout(() => setStep("attract"), WALKAWAY_MS);
    return () => clearTimeout(t);
  }, [step]);

  if (!state) {
    return (
      <main className="screen center">
        <p className="muted">{error || "Menyiapkan booth…"}</p>
      </main>
    );
  }

  const common = { state, refresh, setStep, setError, onTimings };

  return (
    <main className="screen">
      <header>
        <span className="brand">bykami · booth</span>
        <span className="small muted">
          {state.media.low
            ? `Kertas tinggal ${state.media.sheets_remaining} lembar`
            : `${state.media.sheets_remaining} lembar`}
        </span>
      </header>

      {error && <p className="error">{error}</p>}

      {/*
        The simulated provider takes no money and can unlock any session from
        the screen. Announced loudly rather than hidden, so that a booth left in
        this configuration in front of customers is obvious to anyone walking
        past it.
      */}
      {state.flags.payments_simulated && (
        <p className="warn">
          MODE UJI COBA — pembayaran disimulasikan, tidak ada uang yang ditarik.
        </p>
      )}

      {step === "attract" && <Attract {...common} />}
      {step === "packages" && <Packages {...common} setTemplateId={setTemplateId} />}
      {step === "pay" && <Pay {...common} />}
      {step === "frame" && (
        <Frame {...common} templateId={templateId} setTemplateId={setTemplateId} />
      )}
      {step === "capture" && <Capture {...common} templateId={templateId} />}
      {step === "review" && (
        <Review
          {...common}
          templateId={templateId}
          setTemplateId={setTemplateId}
          onPrinted={(photos) => { setSelected(photos); setStep("delivery"); }}
        />
      )}
      {step === "delivery" && <Delivery {...common} selected={selected} />}
      {step === "done" && <Done {...common} />}

      {perf && <PerfOverlay timings={timings} />}
    </main>
  );
}

/**
 * The measurements, on the screen rather than in a console.
 *
 * A booth is tested by standing in front of one, and on a phone or a kiosk
 * panel there is no devtools window to read. Values are the last observed, not
 * an average: what a customer notices is the slow take, not the mean.
 */
function PerfOverlay({ timings }: { timings: Timings }) {
  const rows = Object.entries(timings);
  if (rows.length === 0) return null;

  return (
    <aside className="perf">
      {rows.map(([k, v]) => (
        <div key={k}>
          <span>{labels[k] ?? k}</span>
          <b>{k === "bytes" ? `${Math.round(v / 1024)} KB` : `${v} ms`}</b>
        </div>
      ))}
    </aside>
  );
}

export interface ScreenProps {
  state: State;
  refresh: () => Promise<State | null>;
  setStep: (s: Step) => void;
  setError: (s: string) => void;
  /** Reports a client-side measurement. A no-op unless ?perf=1. */
  onTimings: (t: Timings) => void;
}
