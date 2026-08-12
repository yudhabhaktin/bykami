package camera_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/camera"
)

// fakeTool writes a tiny executable the camera package believes is gphoto2, so
// a test controls exactly what the tool prints and does without a camera or a
// real install. A shell script because the tests run where /bin/sh does.
func fakeTool(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gphoto2")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake tool: %v", err)
	}
	return path
}

const detectCamera = `
if [ "$1" = "--auto-detect" ]; then
  echo "Model                          Port"
  echo "----------------------------------------------------------"
  echo "Canon EOS 200D                 usb:002,006"
  exit 0
fi
exit 1
`

func TestDetectFindsCamera(t *testing.T) {
	cam := camera.New(camera.WithTool(fakeTool(t, detectCamera)))
	dev, err := cam.Detect(t.Context())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if dev.Model != "Canon EOS 200D" {
		t.Errorf("model = %q, want Canon EOS 200D", dev.Model)
	}
	if dev.Port != "usb:002,006" {
		t.Errorf("port = %q, want usb:002,006", dev.Port)
	}
}

// No camera is the ordinary state of a booth between bookings — a value, not a
// failure.
func TestDetectNoCameraIsNotAnError(t *testing.T) {
	cam := camera.New(camera.WithTool(fakeTool(t, `
echo "Model                          Port"
echo "----------------------------------------------------------"
`)))
	dev, err := cam.Detect(t.Context())
	if err != nil {
		t.Fatalf("no camera reported as a failure: %v", err)
	}
	if dev.Model != "" || dev.Port != "" {
		t.Fatalf("got %+v, want no device", dev)
	}
}

// A missing binary is an error, not absence — the two must not look alike to
// the operator.
func TestUnresolvableToolIsAnError(t *testing.T) {
	cam := camera.New(camera.WithTool(filepath.Join(t.TempDir(), "no-such-gphoto2")))
	if _, err := cam.Detect(t.Context()); err == nil {
		t.Fatal("a missing tool was reported as an ordinary empty camera list")
	}
}

func TestCaptureWritesAsyncFrame(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKE_LOG", log)

	// The fake does the one thing gphoto2 is trusted for: it writes the file it
	// was told to, atomically, and says so by exiting 0.
	cam := camera.New(camera.WithTool(fakeTool(t, `
log="${FAKE_LOG:-}"
prev=""
for a in "$@"; do
  [ "$prev" = "--filename" ] && f="$a"
  prev="$a"
  [ -n "$log" ] && printf '%s\n' "$a" >> "$log"
done
if [ -z "$f" ]; then echo "no --filename" >&2; exit 1; fi
printf '\377\330\377\331' > "$f"
exit 0
`)))

	dest := t.TempDir()
	path, err := cam.Capture(t.Context(), dest)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if filepath.Dir(path) != dest {
		t.Errorf("frame landed outside the hot folder: %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("capture did not write %s: %v", path, err)
	}

	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	for _, want := range []string{"--capture-image-and-download", "--force-overwrite"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("tool was not asked to %s", want)
		}
	}
	if !strings.Contains(string(got), path) {
		t.Errorf("the --filename the tool got (%s) is not the path reported (%s)", strings.TrimSpace(string(got)), path)
	}
}

// A camera that refuses must be reported, not photographed silently.
func TestCaptureFailsLoudly(t *testing.T) {
	cam := camera.New(camera.WithTool(fakeTool(t, `
echo "PTP Camera worked as: device busy" >&2
exit 1
`)))
	_, err := cam.Capture(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a capture the tool refused was reported as a photograph taken")
	}
	if !strings.Contains(err.Error(), "device busy") {
		t.Errorf("the tool's reason did not survive into the error: %v", err)
	}
}

// gphoto2 exiting 0 without writing the file is the same lie as a shutter that
// returns 200 with the problem in the text.
func TestZeroExitWithoutAFileIsAFailure(t *testing.T) {
	cam := camera.New(camera.WithTool(fakeTool(t, "exit 0\n")))
	_, err := cam.Capture(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a 0 exit that wrote nothing was reported as a photograph taken")
	}
	if !strings.Contains(err.Error(), "without writing") {
		t.Errorf("unexpected error: %v", err)
	}
}

// gphoto2 sits on a camera that unplugged mid-transfer; the deadline, not the
// tool, is what stops the booth hanging in front of a customer.
func TestCaptureIsBounded(t *testing.T) {
	cam := camera.New(
		camera.WithTool(fakeTool(t, "sleep 5\nexit 0\n")),
		camera.WithTimeout(50*time.Millisecond),
	)
	_, err := cam.Capture(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a hung capture was reported as a photograph taken")
	}
	if !strings.Contains(err.Error(), "deadline exceeded") {
		t.Errorf("want the timeout to be the diagnosis, got: %v", err)
	}
}
