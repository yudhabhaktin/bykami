import { describe, expect, it } from "vitest";

import { pickDevice } from "../apps/kiosk/src/camera";

/**
 * Which camera the booth previews.
 *
 * The bug this guards is not hypothetical: a booth PC has a tethered Canon and
 * a webcam in the lid, `getUserMedia` with no device named hands over the lid
 * one, and the result is a customer posing at the wrong camera while the Canon
 * photographs them from a different angle. Nothing on screen looks wrong until
 * the prints come out.
 */

const device = (label: string, deviceId = label): MediaDeviceInfo =>
  ({ kind: "videoinput", label, deviceId, groupId: "g", toJSON: () => ({}) }) as MediaDeviceInfo;

const booth = [
  device("Integrated Camera (5986:215d)", "integrated"),
  device("EOS Webcam Utility (Canon EOS 200D)", "canon"),
];

describe("pickDevice", () => {
  it("finds the tethered camera on a booth PC that also has a lid webcam", () => {
    expect(pickDevice(booth, "EOS")?.deviceId).toBe("canon");
  });

  it("matches case-insensitively, because nobody types a driver's label exactly", () => {
    expect(pickDevice(booth, "eos webcam")?.deviceId).toBe("canon");
  });

  // The hint is a substring precisely so that it survives the label changing
  // between machines and driver versions — "EOS Webcam Utility" on one, the
  // same plus a model suffix on the next.
  it("matches on a fragment rather than the whole label", () => {
    expect(pickDevice(booth, "Canon")?.deviceId).toBe("canon");
  });

  // Not an error. A camera unplugged mid-session must leave the booth showing
  // a picture from something rather than a dead screen, so the caller falls
  // back to the stream it already has.
  it("returns nothing when the named camera is absent", () => {
    expect(pickDevice(booth, "Nikon")).toBeUndefined();
  });

  it("returns nothing when no camera is named, so the browser's default stands", () => {
    expect(pickDevice(booth, "")).toBeUndefined();
  });

  // A microphone called "EOS" must never be handed to a video element. The
  // browser lists every kind of device from one call.
  it("ignores devices that are not cameras", () => {
    const withMic = [
      { ...device("EOS Webcam Utility", "mic"), kind: "audioinput" } as MediaDeviceInfo,
      device("Integrated Camera", "integrated"),
    ];
    expect(pickDevice(withMic, "EOS")).toBeUndefined();
  });
});
