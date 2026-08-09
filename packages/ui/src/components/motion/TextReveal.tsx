import { domAnimation, LazyMotion, useReducedMotion } from "motion/react";
import * as m from "motion/react-m";
import { useArmed } from "./use-armed";

/** Shared with Reveal — `tween 0.12,0.23,0.5,1` in the reference's own notation. */
const EASE_REVEAL: [number, number, number, number] = [0.12, 0.23, 0.5, 1];

const DURATION = 0.6;
const AMOUNT = 0.5;

/**
 * `y: "120px"` on all three of Chimera's text effects.
 *
 * Far enough that each token is well outside its line box when it starts, which
 * is the point: the words appear to be pushed up into place from under the
 * line, and the mask below is what sells it.
 */
const TRAVEL = 120;

interface Props {
  /**
   * The line, as a prop rather than as children.
   *
   * Astro hands slot content to a framework component as rendered markup, not
   * as a string, so `children` here would arrive as an element and split into
   * nothing. Taking the text directly also keeps the accessible name and the
   * tokens reading from one source.
   */
  text: string;
  /**
   * Chimera tokenizes one heading per character and the rest per word, with the
   * per-character one reserved for its hero. Per word is the safer default —
   * character splitting a long Indonesian heading makes a lot of spans.
   */
  by?: "word" | "character";
  /** Per-token stagger. The reference ships 0.05s and 0.1s. */
  stagger?: number;
  /** Which element the text is set in. The type scale does the rest. */
  as?: "h1" | "h2" | "h3" | "p";
  className?: string;
}

/**
 * Chimera's text effect: tokens pushed up into place, staggered, once.
 *
 * The tokens are wrapped in a masking span with `overflow: hidden`, so a token
 * that has not arrived yet is clipped by its own line rather than floating
 * 120px below it. Without the mask this reads as text sliding around the page;
 * with it, as text being set.
 *
 * Accessibility: the split is decoration and assistive technology should never
 * see it. The container carries the whole string as its accessible name and
 * every token span is hidden from the tree, so a screen reader reads one
 * sentence rather than a list of words. Selection and find-in-page still work,
 * because the tokens are real text nodes in document order.
 *
 * Word joining uses a trailing space inside each span rather than a gap on the
 * container. A gap would be dropped by copy-paste and by find-in-page, which
 * would quietly make the heading unsearchable on the page it titles.
 */
export default function TextReveal({
  text,
  by = "word",
  stagger = 0.05,
  as = "h2",
  className,
}: Props) {
  const armed = useArmed();
  const reduced = useReducedMotion();
  const animate = armed && !reduced;

  /*
   * Before arming — which is the server render, the first client render, and
   * every render under reduced motion — this is a plain heading with its text
   * in it and no spans at all. That is the copy crawlers index and readers get
   * if the island never hydrates.
   */
  if (!animate) {
    const Plain = as;
    return (
      <Plain className={className} data-reveal={reduced ? undefined : ""}>
        {text}
      </Plain>
    );
  }

  const Tag = m[as];
  const tokens = by === "word" ? text.split(" ") : [...text];

  return (
    <LazyMotion features={domAnimation} strict>
      <Tag
        className={className}
        data-reveal=""
        aria-label={text}
        initial="hidden"
        whileInView="shown"
        viewport={{ once: true, amount: AMOUNT }}
        variants={{ hidden: { opacity: 0 }, shown: { opacity: 1 } }}
        transition={{ duration: 0.01, staggerChildren: stagger }}
      >
        {tokens.map((token, i) => (
          <span
            key={i}
            aria-hidden="true"
            style={{ display: "inline-block", overflow: "hidden", verticalAlign: "bottom" }}
          >
            <m.span
              style={{ display: "inline-block", willChange: "transform" }}
              variants={{ hidden: { y: TRAVEL }, shown: { y: 0 } }}
              transition={{ duration: DURATION, ease: EASE_REVEAL }}
            >
              {token}
              {by === "word" && i < tokens.length - 1 ? " " : ""}
            </m.span>
          </span>
        ))}
      </Tag>
    </LazyMotion>
  );
}
