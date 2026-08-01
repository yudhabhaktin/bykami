import { useEffect, useRef } from "react";
import QRCode from "qrcode";

/**
 * The QR code, drawn locally from the payload the gateway returned.
 *
 * Nothing fetches an image. The booth has to work with the network down, and a
 * QR code served from someone else's host is a dependency in the middle of the
 * one screen where money changes hands.
 *
 * Shared by the two places a customer pays: the session's own code, and the
 * one that buys another sheet at review. They are the same square with the same
 * failure mode, and drawing them from two copies of this would let one of them
 * quietly stop rendering.
 */
export function QRCanvas({
  payload,
  size = 512,
  onError,
}: {
  payload: string;
  size?: number;
  onError: (message: string) => void;
}) {
  const canvas = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    if (!canvas.current || !payload) return;
    void QRCode.toCanvas(canvas.current, payload, {
      errorCorrectionLevel: "M",
      margin: 1,
      width: size,
    }).catch(() => onError("Gagal menampilkan QR. Panggil petugas."));
  }, [payload, size, onError]);

  return (
    <div className="qr">
      <canvas ref={canvas} />
    </div>
  );
}

/** "2:05" — the countdown beside a code that will expire. */
export function countdown(remaining: number): string {
  const mins = Math.floor(remaining / 60);
  return `${mins}:${String(remaining % 60).padStart(2, "0")}`;
}
