/**
 * Hand-drawn botanical marks, inline.
 *
 * Inline SVG rather than image files for the same reason there is no webfont
 * request: the booth PC is offline-first, and a decoration that arrives over
 * the network is a decoration that can fail to arrive. These cost nothing to
 * draw and cannot 404.
 *
 * Flat, open paths at a single stroke weight — no fills, no gradients. The
 * hand-drawn quality is in the asymmetry of the curves, not in a texture,
 * which is what keeps them crisp on a panel of unknown quality.
 *
 * Purely decorative, so `aria-hidden` and no title: a screen reader announcing
 * "sprig" to a customer at a photobooth would be noise, not information.
 */
export function Doodle({
  shape,
  className,
}: {
  shape: "sprig" | "bloom";
  className?: string;
}) {
  return (
    <svg
      className={className}
      viewBox="0 0 100 100"
      fill="none"
      stroke="currentColor"
      strokeWidth={4}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {shape === "sprig" ? <Sprig /> : <Bloom />}
    </svg>
  );
}

/** A leaning stem with leaves alternating down it. */
function Sprig() {
  return (
    <>
      <path d="M50 94 C 46 70, 48 44, 58 12" />
      <path d="M49 74 C 34 72, 26 62, 27 50 C 40 51, 48 60, 49 74 Z" />
      <path d="M52 56 C 66 50, 72 39, 69 27 C 57 31, 51 42, 52 56 Z" />
      <path d="M54 38 C 42 33, 34 24, 36 13 C 47 17, 54 27, 54 38 Z" />
    </>
  );
}

/**
 * A five-petal flower on a stem.
 *
 * One petal, rotated five times about the centre, so the petals cannot overlap
 * each other the way five independently drawn curves did. The whole head is
 * then tilted a few degrees off true — perfect rotational symmetry is what
 * makes a mark read as generated rather than drawn.
 */
function Bloom() {
  return (
    <>
      <path d="M50 60 C 53 72, 51 83, 46 94" />
      <path d="M50 70 C 40 71, 33 78, 31 88" />
      <g transform="rotate(-7 50 38)">
        {[0, 72, 144, 216, 288].map((deg) => (
          <path key={deg} d="M50 30 C 41 24, 40 13, 50 8 C 60 13, 59 24, 50 30 Z"
            transform={`rotate(${deg} 50 38)`} />
        ))}
        <circle cx="50" cy="38" r="7" />
      </g>
    </>
  );
}
