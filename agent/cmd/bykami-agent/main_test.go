package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
)

// The designs compiled into this binary are the ones the operator console
// cannot see any other way: they have no catalogue row, their artwork exists
// only inside the executable, and a booth offers them alongside whatever it
// syncs. If they stop being reportable the console goes quietly back to showing
// four frames while a customer chooses from eleven, which is the failure this
// whole path exists to end.
func TestEveryBuiltInDesignIsReportable(t *testing.T) {
	builtin, err := compose.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	if len(builtin) == 0 {
		t.Fatal("no built-in templates, so there is nothing to report")
	}

	got := reportable(builtin, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != len(builtin) {
		t.Fatalf("reported %d designs for %d templates", len(got), len(builtin))
	}

	for i, d := range got {
		src := builtin[i]
		switch {
		case d.ID != src.ID || d.Name != src.Name:
			t.Errorf("design %d = %q/%q, want %q/%q", i, d.ID, d.Name, src.ID, src.Name)
		case d.Layout != string(src.Layout):
			t.Errorf("%s: layout = %q, want %q", d.ID, d.Layout, src.Layout)
		case len(d.Cells) != len(src.Cells):
			// The console draws the slots from these, over the artwork below.
			t.Errorf("%s: %d cells reported, want %d", d.ID, len(d.Cells), len(src.Cells))
		case d.SHA256 == "" || d.PNG == nil:
			t.Errorf("%s: reported with no artwork, so the console can only show a name", d.ID)
		}
	}
}

// The hash is the whole upload protocol: it is what the server compares against
// what it already holds, and what it verifies the bytes against on arrival.
func TestAReportedHashIsTheHashOfTheReportedBytes(t *testing.T) {
	builtin, err := compose.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}

	for _, d := range reportable(builtin, slog.New(slog.NewTextHandler(io.Discard, nil))) {
		sum := sha256.Sum256(d.PNG)
		if got := hex.EncodeToString(sum[:]); got != d.SHA256 {
			t.Errorf("%s: hash = %s, but the bytes hash to %s", d.ID, d.SHA256, got)
		}
	}
}

// TestCaptureEndToEnd builds the agent binary, starts it as a child process
// with the fake gphoto2 tool, and exercises the full capture flow through
// HTTP. Every scenario starts a fresh booth so the fake tool sees the right
// BYKAMI_FAKE_GPHOTO2 value.
func TestCaptureEndToEnd(t *testing.T) {
	agentBin := buildAgent(t)
	fakeToolBin := buildCameraTestBinary(t)

	scenarios := []struct {
		name       string
		env        string
		wantCode   int
		wantPhotos int
		wantWidth  int
		wantHeight int
		wantDPI    int
	}{
		{
			name:       "present",
			env:        "present",
			wantCode:   http.StatusAccepted,
			wantPhotos: 1,
			wantWidth:  6000,
			wantHeight: 4000,
			wantDPI:    300,
		},
		{name: "absent", env: "absent", wantCode: http.StatusBadGateway, wantPhotos: 0},
		{name: "busy", env: "busy", wantCode: http.StatusBadGateway, wantPhotos: 0},
		{name: "liar", env: "liar", wantCode: http.StatusBadGateway, wantPhotos: 0},
		{name: "hung", env: "hung", wantCode: http.StatusBadGateway, wantPhotos: 0},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			root := t.TempDir()
			hotFolder := t.TempDir()
			port := pickPort(t)

			cmd := exec.Command(agentBin,
				"-addr", "127.0.0.1:"+port,
				"-root", root,
				"-hot-folder", hotFolder,
				"-payments", "sim",
				"-printer", "sim",
				"-source", "hotfolder",
				"-camera-tool", fakeToolBin,
			)
			cmd.Env = append(os.Environ(), "BYKAMI_FAKE_GPHOTO2="+sc.env)
			// Silence booth output so test output stays readable.
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard

			if err := cmd.Start(); err != nil {
				t.Fatalf("start agent: %v", err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})

			base := "http://127.0.0.1:" + port

			// Wait for the booth to answer.
			pollUntil(t, base+"/api/state", http.StatusOK, 5*time.Second)

			// Start a session (empty package_id picks the default).
			resp := postJSON(t, base+"/api/session", `{"package_id":""}`)
			if resp.StatusCode != http.StatusCreated {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("start session: %d %s", resp.StatusCode, body)
			}
			resp.Body.Close()

			// Settle the simulated payment.
			resp = postJSON(t, base+"/api/payment/simulate", "")
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("simulate payment: %d %s", resp.StatusCode, body)
			}
			resp.Body.Close()

			// Poll until the payment state propagates to the session.
			pollPaymentSettled(t, base+"/api/payment", 5*time.Second)

			// Fire the shutter.
			resp = postJSON(t, base+"/api/capture", "")
			if resp.StatusCode != sc.wantCode {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("capture: got %d, want %d: %s", resp.StatusCode, sc.wantCode, body)
			}
			resp.Body.Close()

			// For a successful tethered capture the frame arrives asynchronously
			// through the hot folder. Poll until it lands or we time out.
			var photos []photoListItem
			if sc.wantPhotos > 0 {
				pollPhotosUntil(t, base+"/api/photos", sc.wantPhotos, 5*time.Second)
				photos = listPhotos(t, base+"/api/photos")
			} else {
				// Give the watcher a moment, then confirm nothing arrived.
				time.Sleep(200 * time.Millisecond)
				photos = listPhotos(t, base+"/api/photos")
			}

			if len(photos) != sc.wantPhotos {
				t.Errorf("photos = %d, want %d", len(photos), sc.wantPhotos)
			}
			if sc.wantPhotos > 0 && len(photos) > 0 {
				p := photos[0]
				if p.Width != sc.wantWidth || p.Height != sc.wantHeight {
					t.Errorf("photo size = %dx%d, want %dx%d", p.Width, p.Height, sc.wantWidth, sc.wantHeight)
				}
				if p.PrintDPI != sc.wantDPI {
					t.Errorf("print_dpi = %d, want %d", p.PrintDPI, sc.wantDPI)
				}
			}
		})
	}
}

type photoListItem struct {
	ID       string `json:"id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	PrintDPI int    `json:"print_dpi"`
}

// exeSuffix is what Windows requires of a binary it is asked to spawn. Without
// it the suite builds fine and then fails at exec, on the one platform the
// booth actually runs.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

func buildAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bykami-agent"+exeSuffix())
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build agent: %v\n%s", err, out)
	}
	return bin
}

func buildCameraTestBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "camera.test"+exeSuffix())
	cmd := exec.Command("go", "test", "-c", "./internal/camera", "-o", bin)
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build camera test binary: %v\n%s", err, out)
	}
	return bin
}

func pickPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
}

func pollUntil(t *testing.T, url string, wantCode int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == wantCode {
			resp.Body.Close()
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to return %d", url, wantCode)
}

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(http.MethodPost, url, reqBody)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func pollPaymentSettled(t *testing.T, url string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var result struct {
			Payment struct {
				State string `json:"state"`
			} `json:"payment"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if result.Payment.State == "settled" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for payment to settle at %s", url)
}

func pollPhotosUntil(t *testing.T, url string, want int, timeout time.Duration) []photoListItem {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		photos := listPhotos(t, url)
		if len(photos) >= want {
			return photos
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d photos at %s", want, url)
	return nil
}

func listPhotos(t *testing.T, url string) []photoListItem {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, body)
	}
	var result struct {
		Photos []photoListItem `json:"photos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode photos: %v", err)
	}
	return result.Photos
}

func TestDoctorStates(t *testing.T) {
	agentBin := buildAgent(t)
	fakeToolBin := buildCameraTestBinary(t)

	scenarios := []struct {
		name       string
		tool       string
		env        string
		wantPhrase string
	}{
		{name: "tool_missing", tool: filepath.Join(t.TempDir(), "no-such-gphoto2"), env: "", wantPhrase: "NOT FOUND"},
		{name: "no_device", tool: fakeToolBin, env: "absent", wantPhrase: "no device on bus"},
		{name: "unclaimable", tool: fakeToolBin, env: "unclaimable", wantPhrase: "present but unclaimable"},
		{name: "present", tool: fakeToolBin, env: "present", wantPhrase: "present and claimable"},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cmd := exec.Command(agentBin, "-camera-tool", sc.tool, "doctor")
			if sc.env != "" {
				cmd.Env = append(os.Environ(), "BYKAMI_FAKE_GPHOTO2="+sc.env)
			}
			out, err := cmd.CombinedOutput()
			// The doctor exits 0 for all four states; it is a diagnostic, not a
			// pass/fail gate. The tool_missing case may exit non-zero because
			// LookPath fails, so we only check the output text.
			_ = err
			if !bytes.Contains(out, []byte(sc.wantPhrase)) {
				t.Fatalf("output missing %q:\n%s", sc.wantPhrase, out)
			}
		})
	}
}
