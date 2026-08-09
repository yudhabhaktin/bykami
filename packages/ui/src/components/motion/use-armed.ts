import { useEffect, useLayoutEffect, useState } from "react";

/**
 * `useLayoutEffect` on the client, `useEffect` on the server.
 *
 * Astro renders these islands to static HTML at build time, and React logs a
 * warning for every `useLayoutEffect` it encounters while doing so. The effect
 * genuinely does need to be a layout effect in the browser — see below — so the
 * hook is swapped rather than downgraded.
 */
const useIsomorphicLayoutEffect = typeof window === "undefined" ? useEffect : useLayoutEffect;

/**
 * False on the server and for exactly one client render, then true forever.
 *
 * This is what keeps a reveal from swallowing the page. Motion writes its
 * `initial` values into the DOM as inline styles, and because these islands are
 * server-rendered those styles would be baked into the shipped HTML — every
 * animated element would arrive at `opacity: 0` and stay there for anyone whose
 * JavaScript never ran. Withholding the animation props until after mount means
 * the HTML on disk carries no opacity at all, and the hidden state comes only
 * from the `.js [data-reveal]` rule in tokens.css, which by construction cannot
 * apply unless scripting is on.
 *
 * A layout effect and not a plain one, because the gap between them is a frame:
 * `useEffect` fires after paint, so the element would paint hidden by CSS, then
 * paint again once motion took over — visible as a stutter on the first section
 * of the page. `useLayoutEffect` closes that before anything is drawn.
 *
 * The first client render deliberately returns false so it matches the
 * server-rendered HTML exactly and hydration stays quiet.
 */
export const useArmed = (): boolean => {
  const [armed, setArmed] = useState(false);
  useIsomorphicLayoutEffect(() => setArmed(true), []);
  return armed;
};
