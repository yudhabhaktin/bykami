import { domAnimation, LazyMotion, useReducedMotion } from "motion/react";
import * as m from "motion/react-m";
import type { ReactNode } from "react";
import { useArmed } from "./use-armed";

/**
 * Chimera's easing curve, as its node transitions state it: `tween 0.12,0.23,0.5,1`.
 * Fourteen appear effects on its home page share it. Most of the distance is
 * covered in the first third and the rest settles slowly, which is what reads as
 * weight rather than speed.
 *
 * Typed as a fixed-length tuple because motion narrows a cubic bézier to four
 * numbers, and a bare array literal widens to number[].
 */
const EASE_REVEAL: [number, number, number, number] = [0.12, 0.23, 0.5, 1];

/** `tween ... 0.6s` — the duration on every appear effect in the reference. */
const DURATION = 0.6;

/**
 * `threshold: 0.5`. Chimera fires an appear effect when half the element is on
 * screen, not when its first pixel is. On a tall section that is a noticeably
 * later trigger, and it is why its reveals feel deliberate instead of trailing
 * the scroll.
 */
const AMOUNT = 0.5;

interface Props {
  children: ReactNode;
  /**
   * Travel, in pixels. Chimera ships four distances — 16, 24, 32 and 48 — and
   * uses them structurally: 16 for small type, 24 for most blocks, 32 and 48
   * for whole sections and the elements it wants to arrive last.
   */
  y?: 16 | 24 | 32 | 48;
  /**
   * Stagger, in seconds. The reference's own steps are 0, 0.07, 0.1, 0.15 and
   * 0.2 — hand-set per element rather than computed off an index, which is why
   * this is a prop and not a child index.
   */
  delay?: number;
  /** Escape hatch for layout, since the wrapper is a real element in the flow. */
  className?: string;
}

/**
 * The appear effect: fade and rise, once, when half in view.
 *
 * `replay: false` in Chimera, so `once: true` here — scrolling back up does not
 * re-run it. That is the single biggest difference from the scroll-driven CSS
 * reveal this replaces, which was tied to scroll position and played backwards
 * as you left the section.
 *
 * Under `prefers-reduced-motion` this renders a plain wrapper: no animation
 * props, and no `data-reveal`, so the CSS never hides it either. Both halves of
 * the effect have to stand down together or the content stays invisible.
 */
export default function Reveal({ children, y = 24, delay = 0, className }: Props) {
  const armed = useArmed();
  const reduced = useReducedMotion();
  const animate = armed && !reduced;

  return (
    <LazyMotion features={domAnimation} strict>
      <m.div
        className={className}
        data-reveal={reduced ? undefined : ""}
        /*
         * The travel distance is handed to CSS rather than to motion. There is
         * no `initial` prop here on purpose: this element mounts before it is
         * armed, so motion would ignore one — see the `.js [data-reveal]` rule
         * in tokens.css, which holds the whole starting state instead. Motion
         * reads the element's current transform when the effect fires and
         * animates out of it.
         *
         * An attribute rather than an inline custom property, because motion
         * types `style` as its own MotionStyle and Preact's CSSProperties does
         * not satisfy it — and because the four legal distances are Chimera's,
         * so they belong in the stylesheet with the rest of the system rather
         * than being interpolated here.
         */
        data-reveal-y={y}
        {...(animate
          ? {
              whileInView: { opacity: 1, y: 0 },
              viewport: { once: true, amount: AMOUNT },
              transition: { duration: DURATION, ease: EASE_REVEAL, delay },
            }
          : {})}
      >
        {children}
      </m.div>
    </LazyMotion>
  );
}
