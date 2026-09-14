package camera_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/camera"
)

// TestMain implements the helper-process pattern. When BYKAMI_FAKE_GPHOTO2 is
// set, the test binary re-executes itself as the fake gphoto2 tool instead of
// running tests. This is portable: no /bin/sh, no sleep binary, nothing that
// does not exist on Windows.
func TestMain(m *testing.M) {
	scenario := os.Getenv("BYKAMI_FAKE_GPHOTO2")
	if scenario == "" {
		os.Exit(m.Run())
	}
	runFakeTool(scenario)
	os.Exit(0)
}

// runFakeTool parses os.Args as gphoto2 would see them and acts out one
// scenario. Every path exits; none falls through to the test runner.
func runFakeTool(scenario string) {
	args := os.Args[1:]
	isDetect := len(args) > 0 && args[0] == "--auto-detect"
	isSummary := len(args) > 0 && args[0] == "--summary"
	isCapture := len(args) > 0 && args[0] == "--capture-image-and-download"

	var filename string
	for i, a := range args {
		if a == "--filename" && i+1 < len(args) {
			filename = args[i+1]
		}
	}

	switch scenario {
	case "present":
		if isDetect {
			fmt.Println("Model                          Port")
			fmt.Println("----------------------------------------------------------")
			fmt.Println("Canon EOS 200D                 usb:002,006")
			os.Exit(0)
		}
		if isSummary {
			fmt.Println("Camera summary:")
			fmt.Println("Manufacturer: Canon Inc.")
			os.Exit(0)
		}
		if isCapture {
			if filename == "" {
				fmt.Fprintln(os.Stderr, "no --filename")
				os.Exit(1)
			}
			// The invocation is part of the contract, so the fake refuses a
			// wrong one rather than quietly succeeding: gphoto2 has to be asked
			// to capture and download, and told to overwrite, or a camera
			// package that asked wrongly would still look correct from here.
			overwrite := false
			for _, a := range args {
				if a == "--force-overwrite" {
					overwrite = true
				}
			}
			if args[0] != "--capture-image-and-download" || !overwrite {
				fmt.Fprintf(os.Stderr, "wrong invocation: %v\n", args)
				os.Exit(1)
			}
			if err := writeRealJPEG(filename); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "unknown args")
		os.Exit(1)

	case "absent":
		if isDetect {
			fmt.Println("Model                          Port")
			fmt.Println("----------------------------------------------------------")
			os.Exit(0)
		}
		if isCapture {
			fmt.Fprintln(os.Stderr, "*** Error ***")
			fmt.Fprintln(os.Stderr, "Could not detect any camera")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "unknown args")
		os.Exit(1)

	case "busy":
		if isDetect {
			fmt.Println("Model                          Port")
			fmt.Println("----------------------------------------------------------")
			fmt.Println("Canon EOS 200D                 usb:002,006")
			os.Exit(0)
		}
		if isCapture {
			fmt.Fprintln(os.Stderr, "Could not lock the device: device busy")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "unknown args")
		os.Exit(1)

	case "liar":
		if isCapture {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "unknown args")
		os.Exit(1)

	case "hung":
		if isCapture {
			time.Sleep(5 * time.Second)
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "unknown args")
		os.Exit(1)

	case "unclaimable":
		if isDetect {
			fmt.Println("Model                          Port")
			fmt.Println("----------------------------------------------------------")
			fmt.Println("Canon EOS 200D                 usb:002,006")
			os.Exit(0)
		}
		if isSummary {
			fmt.Fprintln(os.Stderr, "Could not claim the USB device")
			os.Exit(1)
		}
		if isCapture {
			fmt.Fprintln(os.Stderr, "Could not claim the USB device")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "unknown args")
		os.Exit(1)

	default:
		fmt.Fprintf(os.Stderr, "unknown scenario %q\n", scenario)
		os.Exit(1)
	}
}

// writeRealJPEG encodes a 6000×4000 grayscale image using the standard library
// jpeg encoder. The pixel content does not matter; the dimensions do.
func writeRealJPEG(path string) error {
	img := image.NewGray(image.Rect(0, 0, 6000, 4000))
	// Fill with a simple gradient so the file is not empty-looking if opened.
	for y := range 4000 {
		for x := range 6000 {
			img.SetGray(x, y, color.Gray{Y: uint8((x + y) / 47)})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func fakeTool(t *testing.T) string {
	t.Helper()
	return os.Args[0]
}

func TestDetectFindsCamera(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "present")
	cam := camera.New(camera.WithTool(fakeTool(t)))
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

func TestDetectNoCameraIsNotAnError(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "absent")
	cam := camera.New(camera.WithTool(fakeTool(t)))
	dev, err := cam.Detect(t.Context())
	if err != nil {
		t.Fatalf("no camera reported as a failure: %v", err)
	}
	if dev.Model != "" || dev.Port != "" {
		t.Fatalf("got %+v, want no device", dev)
	}
}

func TestUnresolvableToolIsAnError(t *testing.T) {
	cam := camera.New(camera.WithTool(filepath.Join(t.TempDir(), "no-such-gphoto2")))
	if _, err := cam.Detect(t.Context()); err == nil {
		t.Fatal("a missing tool was reported as an ordinary empty camera list")
	}
}

func TestCaptureWritesRealJPEG(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "present")
	cam := camera.New(camera.WithTool(fakeTool(t)))
	dest := t.TempDir()
	path, err := cam.Capture(t.Context(), dest)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if filepath.Dir(path) != dest {
		t.Errorf("frame landed outside the hot folder: %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("capture did not write %s: %v", path, err)
	}
	if fi.Size() == 0 {
		t.Error("capture wrote an empty file")
	}

	// Verify it is a real JPEG with the right dimensions.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatalf("file is not a valid JPEG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != 6000 || b.Dy() != 4000 {
		t.Errorf("dimensions = %dx%d, want 6000x4000", b.Dx(), b.Dy())
	}
}

func TestCaptureFailsLoudly(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "busy")
	cam := camera.New(camera.WithTool(fakeTool(t)))
	_, err := cam.Capture(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a capture the tool refused was reported as a photograph taken")
	}
	if !strings.Contains(err.Error(), "device busy") {
		t.Errorf("the tool's reason did not survive into the error: %v", err)
	}
}

func TestZeroExitWithoutAFileIsAFailure(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "liar")
	cam := camera.New(camera.WithTool(fakeTool(t)))
	_, err := cam.Capture(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("a 0 exit that wrote nothing was reported as a photograph taken")
	}
	if !strings.Contains(err.Error(), "without writing") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCaptureIsBounded(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "hung")
	cam := camera.New(
		camera.WithTool(fakeTool(t)),
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

func TestSummarySucceedsWhenCameraIsClaimable(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "present")
	cam := camera.New(camera.WithTool(fakeTool(t)))
	if err := cam.Summary(t.Context()); err != nil {
		t.Fatalf("summary: %v", err)
	}
}

func TestSummaryFailsWhenCameraIsUnclaimable(t *testing.T) {
	t.Setenv("BYKAMI_FAKE_GPHOTO2", "unclaimable")
	cam := camera.New(camera.WithTool(fakeTool(t)))
	err := cam.Summary(t.Context())
	if err == nil {
		t.Fatal("expected error for unclaimable camera")
	}
	if !strings.Contains(err.Error(), "Could not claim") {
		t.Errorf("expected claim error, got: %v", err)
	}
}
