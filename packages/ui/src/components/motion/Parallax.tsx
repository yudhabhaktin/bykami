import {
  domAnimation,
  LazyMotion,
  useReducedMotion,
  useScroll,
  useSpring,
  useTransform,
} from "motion/react";
import * as m from "motion/react-m";
import type { ReactNode } from "react";
import { useRef } from "react";
import { useArmed } from "./use-armed";

/**
 * `spring-physics 250 60 1` — the transition on Chimera's scroll transform,
 * stated as stiffness, damping and mass. Damping 60 against stiffness 250 is
 * overdamped, so the value trails the scroll and settles without ever
 * overshooting it. That lag is the effect; a parallax locked exactly to scroll
 * position reads as a rendering artefact rather than as depth.
 */
const SPRING = { stiffness: 250, damping: 60, mass: 1 } as const;

/** `{ y: "-160px" }` and `{ y: "160px" }` — the pair the reference moves between. */
const TRAVEL = 160;

interface Props {
  children: ReactNode;
  /**
   * Which way the element drifts against the scroll. Chimera runs one column
   * up and its neighbour down, which is what makes the two read as separate
   * planes rather than as one slow layer.
   */
  direction?: "up" | "down";
  /** Fraction of the reference's 160px. Useful for smaller elements. */
  strength?: number;
  className?: string;
}

/**
 * Scroll-linked drift.
 *
 * No `data-reveal`, because nothing here is ever hidden — but it is armed like
 * the others, and for a reason that only showed up in the built HTML. The
 * transform is live from the first render, and at scroll progress zero that is
 * the top of the travel, so the server-rendered markup carried a literal
 * `translateY(80px)`: with no JavaScript the strip sat 80px below where the
 * layout put it and stayed there. Withholding the style until after mount
 * means the shipped HTML has the element at rest in its normal position, which
 * is what a visitor with no JavaScript should get — a page that does not drift.
 *
 * `offset` runs from the element's top meeting the bottom of the viewport to
 * its bottom meeting the top — the whole time it is on screen — so the travel
 * is spent on the pass rather than finishing before the element is in view.
 */
export default function Parallax({
  children,
  direction = "up",
  strength = 1,
  className,
}: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const armed = useArmed();
  const reduced = useReducedMotion();

  const { scrollYProgress } = useScroll({
    target: ref,
    offset: ["start end", "end start"],
  });

  const distance = TRAVEL * strength * (direction === "up" ? -1 : 1);
  const target = useTransform(scrollYProgress, [0, 1], [-distance, distance]);
  const y = useSpring(target, SPRING);

  return (
    <LazyMotion features={domAnimation} strict>
      <m.div ref={ref} className={className} style={armed && !reduced ? { y } : undefined}>
        {children}
      </m.div>
    </LazyMotion>
  );
}
