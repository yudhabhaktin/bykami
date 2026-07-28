import { useCallback, useEffect, useState } from "react";

import { api, ApiError, type Photo, type State } from "./api";
import { initPerf, labels, perfEnabled, type Timings } from "./perf";
import { Capture } from "./screens/Capture";
import { Delivery } from "./screens/Delivery";
import { Done } from "./screens/Done";
import { Idle } from "./screens/Idle";
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
 */
export type Step = "idle" | "pay" | "capture" | "review" | "delivery" | "done";

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [step, setStep] = useState<Step>("idle");
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Photo[]>([]);
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
      if (!next.session) return setStep("idle");
      setStep(next.session.state === "awaiting_payment" ? "pay" : "capture");
    })();
  }, [refresh]);

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

      {step === "idle" && <Idle {...common} />}
      {step === "pay" && <Pay {...common} />}
      {step === "capture" && <Capture {...common} />}
      {step === "review" && (
        <Review {...common} onPrinted={(photos) => { setSelected(photos); setStep("delivery"); }} />
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
