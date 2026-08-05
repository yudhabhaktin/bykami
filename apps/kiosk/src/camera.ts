/**
 * Opening the right camera.
 *
 * A booth PC has at least two: the tethered camera the customer is being
 * photographed by, and the webcam built into the lid. `getUserMedia` with no
 * device named hands over whichever the browser calls default, and that is
 * reliably the built-in one — so the screen shows a laptop webcam while the
 * Canon photographs the same person from somewhere else, and nobody notices
 * until the prints do not match what anyone was looking at.
 *
 * Which camera is right is a property of the booth's hardware, so the agent
 * serves it (`-camera`) rather than the bundle deciding.
 */

/** Why the camera could not be opened. The kiosk shows one message either way;
 *  this exists so the reason reaches the log rather than the customer. */
export class CameraError extends Error {
  constructor(
    message: string,
    readonly reason: "insecure" | "denied",
  ) {
    super(message);
  }
}

/**
 * Finds the video device whose label contains `hint`, case-insensitively.
 *
 * Substring rather than exact match because nobody can type the label a driver
 * produces from memory: Canon's virtual camera arrives as "EOS Webcam Utility"
 * on one machine and "EOS Webcam Utility (Canon EOS 200D)" on the next, and an
 * operator writing a service definition knows only that it says EOS somewhere.
 *
 * Returns undefined when nothing matches, which is deliberately not an error —
 * a camera that is unplugged mid-session must leave the booth showing a picture
 * from something rather than a dead screen.
 */
export function pickDevice(
  devices: MediaDeviceInfo[],
  hint: string,
): MediaDeviceInfo | undefined {
  const cameras = devices.filter((d) => d.kind === "videoinput");
  if (!hint) return undefined;

  const want = hint.toLowerCase();
  return cameras.find((d) => d.label.toLowerCase().includes(want));
}

/** What the preview asks the sensor for. The capture path wants the largest
 *  frame it can get; `ideal` rather than `exact` so a camera that cannot do
 *  1080p degrades instead of refusing. */
const SIZE = { width: { ideal: 1920 }, height: { ideal: 1080 } };

/**
 * Opens the camera the booth is configured to preview.
 *
 * The two-step open is forced by the permission model, not chosen: device
 * labels are empty until the page holds a camera permission, so there is no way
 * to find the camera called "EOS" without first opening *a* camera. The first
 * stream is the price of reading the labels. When it turns out to already be
 * the right device — the common case on a booth with one camera, and on every
 * developer laptop — it is kept rather than reopened, because closing and
 * reopening a camera costs a second of black screen.
 */
export async function openCamera(hint: string): Promise<MediaStream> {
  // getUserMedia does not exist on an insecure origin, and the failure is a
  // missing property rather than a rejected promise: `navigator.mediaDevices`
  // is undefined, so reaching for it throws synchronously and lands nowhere
  // near a .catch(). Checked here so that opening the kiosk over plain http
  // says "call staff" instead of unmounting the app to a blank screen.
  if (!navigator.mediaDevices?.getUserMedia) {
    throw new CameraError(
      window.isSecureContext
        ? "browser has no camera API"
        : `camera needs https or localhost; this page is ${window.location.origin}`,
      "insecure",
    );
  }

  const first = await navigator.mediaDevices.getUserMedia({ video: SIZE, audio: false });
  if (!hint) return first;

  const wanted = pickDevice(await navigator.mediaDevices.enumerateDevices(), hint);
  if (!wanted) return first;

  // Already looking at it. Comparing by device id rather than by label: two
  // cameras can report the same label, and the id is what the browser actually
  // opened.
  const open = first.getVideoTracks()[0]?.getSettings().deviceId;
  if (open && open === wanted.deviceId) return first;

  // Released before the second open, not after. A camera is exclusive on
  // Windows, and holding the built-in one while asking for the Canon is how a
  // booth ends up with a NotReadableError on the device it actually wants.
  first.getTracks().forEach((t) => t.stop());

  return navigator.mediaDevices.getUserMedia({
    // exact: having been told which camera this booth photographs with,
    // silently falling back to a different one is the bug this whole module
    // exists to prevent.
    video: { deviceId: { exact: wanted.deviceId }, ...SIZE },
    audio: false,
  });
}
