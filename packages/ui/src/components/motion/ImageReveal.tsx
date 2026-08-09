import { domAnimation, LazyMotion, useReducedMotion } from "motion/react";
import * as m from "motion/react-m";
import type { ReactNode } from "react";
import { useArmed } from "./use-armed";

/**
 * Chimera's image appear effect: `opacity: 0.5, scale: 1.1` settling under
 * `spring-duration 1.5s 0 0s` — a spring described by how long it takes rather
 * than by stiffness, with the bounce dialled out entirely.
 *
 * It never fades from zero. Starting at half opacity means the photograph is
 * legible for the whole of the animation and only resolves, rather than
 * arriving; on a page whose product is photography that is the difference
 * between a reveal and a loading state.
 */
const SETTLED = { opacity: 1, scale: 1 };
const SPRING = { type: "spring", duration: 1.5, bounce: 0 } as const;
const AMOUNT = 0.5;

interface Props {
  children: ReactNode;
  className?: string;
}

/**
 * Wraps a figure or an image so it resolves into place.
 *
 * The wrapper clips, because the child starts 10% oversized and would otherwise
 * push its neighbours around for the length of the spring. It also carries the
 * radius: clipping a scaled child against a square box would show square
 * corners for 1.5s on an image the page rounds.
 *
 * The pre-hydration state is `.js [data-reveal-media]` in tokens.css, set to
 * the same 0.5/1.1 this animates from. That matters more here than it does for
 * a text block — these islands hydrate on visibility, so an image already
 * painted at full size would visibly snap back to 110% at the moment the chunk
 * landed. Matching the two means there is nothing to snap.
 */
export default function ImageReveal({ children, className }: Props) {
  const armed = useArmed();
  const reduced = useReducedMotion();
  const animate = armed && !reduced;

  return (
    <LazyMotion features={domAnimation} strict>
      <m.div
        className={className}
        data-reveal-media={reduced ? undefined : ""}
        style={{ overflow: "hidden", borderRadius: "var(--radius)" }}
        /*
         * No `initial`, for the reason Reveal has none: this element mounts
         * before it is armed and motion would discard one. The 0.5/1.1 starting
         * state is the `.js [data-reveal-media]` rule in tokens.css, and motion
         * animates out of whatever the DOM is showing when the effect fires.
         */
        {...(animate
          ? {
              whileInView: SETTLED,
              viewport: { once: true, amount: AMOUNT },
              transition: SPRING,
            }
          : {})}
      >
        {children}
      </m.div>
    </LazyMotion>
  );
}
