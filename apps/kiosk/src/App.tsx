import { useCallback, useEffect, useState } from "react";

import { api, ApiError, type Photo, type State } from "./api";
import { FilterDefs, NO_FILTER } from "./Filters";
import { initPerf, labels, perfEnabled, type Timings } from "./perf";
import { recall, remember } from "./stash";
import { Attract } from "./screens/Attract";
import { Capture } from "./screens/Capture";
import { Delivery } from "./screens/Delivery";
import { Done } from "./screens/Done";
import { Frame } from "./screens/Frame";
import { Pay } from "./screens/Pay";
import { Review } from "./screens/Review";
import { Session } from "./screens/Session";

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
  | "session"
  | "pay"
  | "frame"
  | "capture"
  | "review"
  | "delivery"
  | "done";

/** How long the photo session runs before the booth moves the customer on. */
const DEFAULT_MINUTES = 5;

export function App() {
  const [state, setState] = useState<State | null>(null);
  const [step, setStep] = useState<Step>("attract");
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Photo[]>([]);
  // Chosen on the frame screen, and no longer changeable afterwards: review is
  // where a customer picks which photographs go in the strip, and re-opening
  // the layout there was a second place to answer a question already answered.
  // Seeded from the package, which is the frame the session opens on.
  const [templateId, setTemplateId] = useState("");
  // Like the template, the filter travels with the print request rather than
  // being committed at capture — so changing your mind at review is free, and
  // the originals on disk stay unfiltered.
  const [filter, setFilter] = useState(NO_FILTER);
  // Whether the printer's blade splits the sheet. Chosen on the session screen
  // and read again when the sheet is queued, so it outlives both. Cut is the
  // default because two strips is the booth format the price is named for.
  const [cut, setCut] = useState(true);
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
      // The choice they made before the reload, not the default. Resuming a
      // "No Cut" session with the blade armed would hand back the wrong thing.
      setCut(recall(next.session.id, "cut") !== "no");
      if (next.session.state === "awaiting_payment") return setStep("pay");
      // Frames already taken means the frame screen has been through once;
      // sending them back to it would look like the booth forgot.
      setStep(next.session.takes > 0 ? "capture" : "session");
    })();
  }, [refresh]);

  // Persisted as it is chosen rather than when the sheet is queued: the reload
  // this survives is the one that happens in between.
  const chooseCut = useCallback(
    (next: boolean) => {
      setCut(next);
      const id = state?.session?.id;
      if (id) remember(id, "cut", next ? "cut" : "no");
    },
    [state],
  );

  if (!state) {
    return (
      <main className="screen center">
        <p className="muted">{error || "Menyiapkan booth…"}</p>
      </main>
    );
  }

  const common = { state, refresh, setStep, setError, onTimings };

  // How long the customer has in front of the camera, taken from what was sold
  // rather than written down here — the number counted down has to be the
  // number the price list advertises.
  const minutes = state.packages[0]?.duration_minutes || DEFAULT_MINUTES;

  return (
    <main className="screen">
      <header>
        <img
          className="brand"
          src="/logo.png"
          alt="studio by KAMI"
          width={640}
          height={210}
        />
        <span className="small muted">
          {state.media.low
            ? `Kertas tinggal ${state.media.sheets_remaining} lembar`
            : `${state.media.sheets_remaining} lembar`}
        </span>
      </header>

      {/* Mounted once, for every screen that previews a photo. */}
      <FilterDefs filters={state.filters} />

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

      {step === "attract" && <Attract {...common} setTemplateId={setTemplateId} />}
      {step === "pay" && <Pay {...common} />}
      {step === "session" && (
        <Session {...common} cut={cut} setCut={chooseCut} minutes={minutes} />
      )}
      {step === "frame" && (
        <Frame {...common} templateId={templateId} setTemplateId={setTemplateId} />
      )}
      {step === "capture" && (
        <Capture {...common} templateId={templateId} minutes={minutes} />
      )}
      {step === "review" && (
        <Review
          {...common}
          templateId={templateId}
          filter={filter}
          setFilter={setFilter}
          cut={cut}
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
