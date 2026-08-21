import { z } from "zod";

/**
 * Every publishable fact carries where it came from.
 *
 * The catalogue lives in `refs/*.pdf`, which is gitignored and owner-copyright,
 * so nothing in this repo is owner-confirmed yet. Publishing an unconfirmed price
 * inside `Offer` schema is worse than publishing no price at all: search engines
 * and LLMs quote structured data back as fact, and a wrong number contradicts the
 * live booking calendar at the moment a customer is deciding.
 *
 * So provenance is part of the type, not a comment:
 *
 *   verified   — owner-confirmed. Renders, and emits structured data.
 *   unverified — read off a PDF or inferred. Renders, never emits structured data.
 *   blocked    — nothing to publish. Does not render.
 *
 * "Nothing to publish" is usually no source at all, and occasionally a fact the
 * owner has a source for and has decided not to put on a page — see
 * QUOTED_PER_JOB in verticals/studio.ts. The `note` is what tells the two apart,
 * so it is worth writing properly: `gaps()` prints it, and a reader deciding
 * whether a blocked fact is still waiting on somebody has nothing else to go on.
 */
export type Sourced<T> =
  | { status: "verified"; value: T; source: string }
  | { status: "unverified"; value: T; source: string }
  | { status: "blocked"; note: string };

export const sourced = <T extends z.ZodType>(value: T) =>
  z.discriminatedUnion("status", [
    z.object({
      status: z.literal("verified"),
      value,
      source: z.string().min(1),
    }),
    z.object({
      status: z.literal("unverified"),
      value,
      source: z.string().min(1),
    }),
    z.object({
      status: z.literal("blocked"),
      note: z.string().min(1),
    }),
  ]);

/** Owner-confirmed. The only status allowed into JSON-LD. */
export const isPublishable = <T>(
  s: Sourced<T>,
): s is { status: "verified"; value: T; source: string } =>
  s.status === "verified";

/** Has a value to show a human, whether or not it is confirmed. */
export const isRenderable = <T>(
  s: Sourced<T>,
): s is
  | { status: "verified"; value: T; source: string }
  | { status: "unverified"; value: T; source: string } =>
  s.status !== "blocked";

/** The value if there is one, otherwise undefined. Never throws. */
export const valueOf = <T>(s: Sourced<T>): T | undefined =>
  isRenderable(s) ? s.value : undefined;

/**
 * The value only if it may be published. Callers building structured data must
 * use this rather than `valueOf`, so an unverified price cannot reach an `Offer`
 * through an accidental read.
 */
export const publishedValueOf = <T>(s: Sourced<T>): T | undefined =>
  isPublishable(s) ? s.value : undefined;

export const verified = <T>(value: T, source: string): Sourced<T> => ({
  status: "verified",
  value,
  source,
});

export const unverified = <T>(value: T, source: string): Sourced<T> => ({
  status: "unverified",
  value,
  source,
});

export const blocked = <T>(note: string): Sourced<T> => ({
  status: "blocked",
  note,
});
