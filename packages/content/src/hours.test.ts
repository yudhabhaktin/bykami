import { describe, expect, it } from "vitest";
import { openingHoursText } from "./hours.ts";

describe("opening hours read as a sentence, not as schema.org", () => {
  it("calls a full week what a person would call it", () => {
    expect(openingHoursText(["Mo-Su 09:00-21:00"])).toBe("Setiap hari 09.00–21.00");
  });

  it("names a partial range in Indonesian", () => {
    expect(openingHoursText(["Mo-Fr 09:00-17:00"])).toBe("Senin–Jumat 09.00–17.00");
  });

  it("handles a single day", () => {
    expect(openingHoursText(["Sa 10:00-14:00"])).toBe("Sabtu 10.00–14.00");
  });

  it("joins several entries", () => {
    expect(openingHoursText(["Mo-Fr 09:00-17:00", "Sa 10:00-14:00"])).toBe(
      "Senin–Jumat 09.00–17.00, Sabtu 10.00–14.00",
    );
  });

  /**
   * The important one. Anything unrecognised has to survive intact: dropping it
   * under-reports when the studio is open and guessing at it misreports, and
   * both are worse than showing the raw string to the handful of people who
   * would ever see it.
   */
  it("returns anything it does not recognise unchanged", () => {
    expect(openingHoursText(["Mo-Su 09:00-21:00 except Lebaran"])).toBe(
      "Mo-Su 09:00-21:00 except Lebaran",
    );
    expect(openingHoursText(["24/7"])).toBe("24/7");
  });

  it("does not turn a non-week range into 'setiap hari'", () => {
    expect(openingHoursText(["Tu-Su 09:00-21:00"])).toBe("Selasa–Minggu 09.00–21.00");
  });
});
