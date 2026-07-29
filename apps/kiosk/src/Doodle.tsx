/**
 * Hand-drawn marks, inline.
 *
 * Inline SVG rather than image files for the same reason there is no webfont
 * request: the booth PC is offline-first, and a decoration that arrives over
 * the network is a decoration that can fail to arrive. These cost nothing to
 * draw and cannot 404.
 *
 * Flat, open paths at a single stroke weight — no fills, no gradients. The
 * hand-drawn quality is in the asymmetry of the curves, not in a texture,
 * which is what keeps them crisp on a panel of unknown quality. Every shape is
 * drawn in a 100×100 box so they can be swapped anywhere without relayout.
 *
 * The set started as two botanicals on the attract screen and stayed there,
 * which left the six screens a paying customer actually walks through looking
 * like a form. These are the rest of the vocabulary: still one line weight,
 * still no fill, but cheerful enough to be worth looking at while a countdown
 * runs.
 *
 * Purely decorative, so `aria-hidden` and no title: a screen reader announcing
 * "sprig" to a customer at a photobooth would be noise, not information.
 */
export type DoodleShape =
  | "sprig"
  | "bloom"
  | "burst"
  | "heart"
  | "rainbow"
  | "cloud"
  | "camera"
  | "squiggle";

export function Doodle({ shape, className }: { shape: DoodleShape; className?: string }) {
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
      {shapes[shape]()}
    </svg>
  );
}

const shapes: Record<DoodleShape, () => React.ReactElement> = {
  sprig: Sprig,
  bloom: Bloom,
  burst: Burst,
  heart: Heart,
  rainbow: Rainbow,
  cloud: Cloud,
  camera: Camera,
  squiggle: Squiggle,
};

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
          <path
            key={deg}
            d="M50 30 C 41 24, 40 13, 50 8 C 60 13, 59 24, 50 30 Z"
            transform={`rotate(${deg} 50 38)`}
          />
        ))}
        <circle cx="50" cy="38" r="7" />
      </g>
    </>
  );
}

/**
 * A four-point sparkle with two smaller ones orbiting it.
 *
 * The concave sides are what make it read as a sparkle rather than a plus sign
 * — each arm is two curves pulled towards the centre. Used wherever the screen
 * is congratulating somebody.
 */
function Burst() {
  return (
    <>
      <path d="M44 44 C 45 30, 46 20, 47 8 C 49 20, 50 30, 51 44 C 64 45, 74 46, 86 48 C 74 50, 64 51, 51 52 C 50 65, 49 76, 47 88 C 46 76, 45 65, 44 52 C 31 51, 21 50, 9 48 C 21 46, 31 45, 44 44 Z" />
      <path d="M76 16 C 77 12, 77 10, 78 7 C 79 10, 79 12, 80 16 C 84 17, 86 17, 89 18 C 86 19, 84 19, 80 20 C 79 24, 79 26, 78 29 C 77 26, 77 24, 76 20 C 72 19, 70 19, 67 18 C 70 17, 72 17, 76 16 Z" />
      <path d="M20 74 C 21 71, 21 69, 22 67 C 23 69, 23 71, 24 74 C 27 75, 29 75, 31 76 C 29 77, 27 77, 24 78 C 23 81, 23 83, 22 85 C 21 83, 21 81, 20 78 C 17 77, 15 77, 13 76 C 15 75, 17 75, 20 74 Z" />
    </>
  );
}

/** A heart, drawn slightly lopsided so it reads as hand-made. */
function Heart() {
  return (
    <path d="M50 88 C 20 66, 8 50, 10 34 C 12 20, 27 12, 38 20 C 44 24, 48 30, 50 36 C 52 30, 57 24, 63 20 C 74 13, 89 21, 90 35 C 91 51, 79 67, 50 88 Z" />
  );
}

/** Three arcs and a pair of clouds. The gaps between the bands are the charm. */
function Rainbow() {
  return (
    <>
      <path d="M12 74 A 38 38 0 0 1 88 74" />
      <path d="M25 74 A 25 25 0 0 1 75 74" />
      <path d="M38 74 A 12 12 0 0 1 62 74" />
      <path d="M8 74 C 4 74, 2 78, 5 81 C 8 84, 16 84, 19 81" />
      <path d="M92 74 C 96 74, 98 78, 95 81 C 92 84, 84 84, 81 81" />
    </>
  );
}

/** A cloud, bumpier on one side than the other. */
function Cloud() {
  return (
    <path d="M26 72 C 14 72, 8 64, 11 55 C 13 48, 21 44, 28 46 C 29 33, 41 25, 52 29 C 59 31, 64 37, 65 44 C 76 41, 87 48, 87 59 C 87 67, 80 72, 71 72 Z" />
  );
}

/**
 * A compact camera with a bloom of flash lines.
 *
 * The one representational mark in the set, and it earns that by being the
 * thing the whole machine does. Kept to the same open-path idiom rather than
 * drawn as an icon, so it sits beside the botanicals instead of on top of them.
 */
function Camera() {
  return (
    <>
      <path d="M12 40 C 12 35, 15 32, 20 32 L 32 32 L 38 24 L 62 24 L 68 32 L 80 32 C 85 32, 88 35, 88 40 L 88 70 C 88 75, 85 78, 80 78 L 20 78 C 15 78, 12 75, 12 70 Z" />
      <circle cx="50" cy="54" r="15" />
      <circle cx="50" cy="54" r="6" />
      <circle cx="76" cy="42" r="3" />
    </>
  );
}

/** A loose horizontal wave. A divider that is not a straight line. */
function Squiggle() {
  return <path d="M6 52 C 20 34, 32 34, 44 52 C 56 70, 68 70, 80 52 C 86 43, 91 41, 96 44" />;
}
