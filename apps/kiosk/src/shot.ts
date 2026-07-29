import type { Template } from "./api";

/**
 * The shape of the photo the customer is actually taking.
 *
 * A webcam hands over 16:9 and a frame's cells are usually taller than they are
 * wide, and `compose.drawCover` resolves that by filling the cell and throwing
 * the overflow away. So a preview showing the whole sensor is a preview showing
 * pixels that will never be printed: people frame themselves against the edges
 * they can see, and then find the print cropped into their shoulders.
 *
 * Cropping the *preview* to the cell instead is what makes the two agree. The
 * file on disk is still the full frame — nothing is discarded at capture — so
 * switching frame at review re-crops from everything the camera saw rather than
 * from an already-cropped copy.
 *
 * The first cell decides it. Nearly every frame is a uniform grid, and for the
 * ones that are not there is no single honest answer: the customer has not yet
 * chosen which photo lands in which hole, and that choice is made at review.
 */
export function cellAspect(template: Template | undefined): string | undefined {
  const c = template?.cells[0];
  if (!c || c.w <= 0 || c.h <= 0) return undefined;
  return `${c.w} / ${c.h}`;
}
