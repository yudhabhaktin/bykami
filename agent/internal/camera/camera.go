// Package camera drives a Canon EOS (or any gphoto2-supported body) attached
// by USB, through the gphoto2 command-line tool.
//
// The camera stays at arm's length on purpose: the agent shells out to gphoto2
// rather than linking libgphoto2, which is what keeps this a pure GOOS=windows
// cross-compile with no cgo and no Windows build host. That indirection is not
// free — libgphoto2 stands behind the binary, and on Windows it only sees a
// Canon over the libusb driver (Zadig) rather than Canon's own, which is a box
// an operator has to tick once — but it is the difference between a subprocess
// and a vendor SDK in the build, and the latter is what design/kiosk.md has now
// rejected twice.
//
// Two operations, each one subprocess. Detect answers "is the booth seeing the
// camera?" for the operator; Capture fires it and puts the frame where the
// ingest watcher already looks, so a photo it takes is treated like any other.
package camera

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTimeout bounds one capture.
//
// Generous, because a capture is a real photograph and a booth that gave up at
// two seconds would report a failure for frames it had taken. Bounded all the
// same: gphoto2 is known to sit and wait on a camera that unplugged
// mid-transfer, and a countdown that has ended has a customer standing in front
// of it — the one thing that must not happen is the screen hanging with no
// explanation.
const DefaultTimeout = 30 * time.Second

// Device is one camera the tool sees on the bus.
type Device struct {
	// Model is what the tool calls the body, e.g. "Canon EOS 200D".
	Model string
	// Port is the usb:bus,device string --auto-detect prints. Deliberately not
	// cached across calls: bus/device numbers change on every re-enumeration,
	// so a port saved at startup is a port that silently names yesterday's
	// plug. Capture re-detects instead.
	Port string
}

// Camera drives one gphoto2 binary.
type Camera struct {
	tool    string
	timeout time.Duration
}

type Option func(*Camera)

// WithTool names the gphoto2 binary. The default, "gphoto2", resolves on PATH.
func WithTool(path string) Option { return func(c *Camera) { c.tool = path } }

// WithTimeout bounds one capture.
func WithTimeout(d time.Duration) Option {
	return func(c *Camera) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// New builds a camera driver.
//
// There is nothing to validate here, unlike a shutter URL: the binary is
// resolved per invocation, so a typo surfaces as the first capture failing
// loudly rather than a booth that refuses to start.
func New(opts ...Option) *Camera {
	c := &Camera{tool: "gphoto2", timeout: DefaultTimeout}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Detect reports whether the tool can see a camera, and which one.
//
// A missing camera is (zero Device, nil), not an error: absence is the ordinary
// state of a booth between bookings, and the caller decides it matters. A
// non-nil error is the tool being unresolvable or refusing to run.
func (c *Camera) Detect(ctx context.Context) (Device, error) {
	out, err := c.run(ctx, "--auto-detect")
	if err != nil {
		return Device{}, err
	}
	return firstDevice(out)
}

// Capture fires the camera and downloads the frame into dest, returning the
// path it was written to.
//
// dest is the hot folder, deliberately: the ingest watcher already makes every
// other safeguard real — hash dedup, the End-Of-Image check, session
// attribution, recovery — and a frame that skipped all of it to take a
// different road in would be a second pipeline with its own bugs. gphoto2
// writes the file atomically once capture is done, so the settle-and-EOI
// ceremony meant for vendor software that writes progressively is not needed
// here.
func (c *Camera) Capture(ctx context.Context, dest string) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dest, fmt.Sprintf("bykami-%d.jpg", time.Now().UnixNano()))

	// No --port: a booth has one camera, and the tool's own first-detected
	// target is the current one rather than a cached usb:bus,dev that a replug
	// has made stale.
	if _, err := c.run(ctx,
		"--capture-image-and-download",
		"--filename", target,
		"--force-overwrite",
	); err != nil {
		return "", err
	}

	// The exit code is not the whole answer — a tool can return 0 without
	// having written the file it was asked for. Presence is checked rather than
	// assumed, the same reason the shutter reads the body at all.
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("camera: capture finished without writing %s: %w", target, err)
	}
	return target, nil
}

// run executes one gphoto2 invocation and returns stdout, folding stderr into
// the error on failure so the reason survives into the log.
func (c *Camera) run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.tool, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			// The process died to the deadline, so its own stderr is noise.
			// The timeout is the diagnosis.
			return nil, fmt.Errorf("camera: %s: %w", strings.Join(args, " "), ctx.Err())
		}
		why := strings.TrimSpace(stderr.String())
		if why == "" {
			why = err.Error()
		}
		return nil, fmt.Errorf("camera: %s: %s", strings.Join(args, " "), why)
	}
	return out, nil
}

// firstDevice parses --auto-detect's table.
//
//	Model                          Port
//	----------------------------------------------------------
//	Canon EOS 200D                 usb:002,006
//
// The first data row is the answer; a header, a separator, and a bare "no
// camera" all yield none. The port is recognised rather than the model because
// it is the machine-readable token — every data row carries one and nothing
// else does.
func firstDevice(out []byte) (Device, error) {
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		port := fields[len(fields)-1]
		if !strings.HasPrefix(port, "usb:") {
			continue
		}
		model := strings.TrimSpace(strings.TrimSuffix(line, port))
		return Device{Model: model, Port: port}, nil
	}
	return Device{}, nil
}