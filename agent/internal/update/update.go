// Package update polls GitHub Releases for a newer agent binary, downloads and
// verifies it, and swaps it in. It is opt-in behind a flag and ticker-driven,
// like framesync.
//
// # Never restart out from under a customer
//
// The updater reads the local /api/state before installing. A non-nil session
// means somebody has paid and is standing at the machine; no answer means the
// booth cannot be asked and the safe reading is "busy". Both defer the
// installation. The deferral is bounded so an abandoned session cannot block
// updates forever.
//
// # The version file
//
// After a successful install and health check the tag is written to a version
// file. On the next poll the tag in that file is compared against the newest
// release; when they match there is nothing to do.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/release"
)

// DefaultInterval between polls.
const DefaultInterval = 10 * time.Minute

// DefaultMaxDefer is how long an update may be deferred while the booth is
// busy. After this it installs anyway.
const DefaultMaxDefer = 60 * time.Minute

// Swapper is the platform-specific half that replaces the running binary and
// restarts the service.
type Swapper interface {
	// Swap moves verifiedNew into place as the running binary, preserving the
	// outgoing one so a rollback is possible. It does not restart anything.
	Swap(verifiedNew string) error
	// Restart starts the new binary. Called after Swap and before the health
	// check so that the health check is exercising the new image.
	Restart() error
	// Rollback restores the previous binary after a failed health check.
	Rollback() error
}

// Worker polls for updates and installs them.
type Worker struct {
	repo         string
	assetName    string
	binPath      string
	versionFile  string
	deferFile    string
	interval     time.Duration
	maxDefer     time.Duration
	stateURL     string
	listURL      string
	downloadBase string
	swapper      Swapper
	log          *slog.Logger
	client       *http.Client
}

// New returns a worker, or nil if updating is not configured.
func New(repo, binPath, versionFile, stateURL string, interval, maxDefer time.Duration, swapper Swapper, log *slog.Logger) *Worker {
	if repo == "" {
		return nil
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	if maxDefer <= 0 {
		maxDefer = DefaultMaxDefer
	}
	return &Worker{
		repo:        repo,
		assetName:   defaultAssetName,
		binPath:     binPath,
		versionFile: versionFile,
		deferFile:   versionFile + ".defer-since",
		interval:    interval,
		maxDefer:    maxDefer,
		stateURL:    stateURL,
		swapper:     swapper,
		log:         log,
		client:      &http.Client{Timeout: 60 * time.Second},
	}
}

// Run polls until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.interval)
	defer t.Stop()

	for {
		if err := w.Poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
			w.log.Warn("update poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Poll checks once.
func (w *Worker) Poll(ctx context.Context) error {
	current, err := w.currentVersion()
	if err != nil {
		return fmt.Errorf("read current version: %w", err)
	}

	releases, err := w.fetchList(ctx)
	if err != nil {
		return fmt.Errorf("fetch releases: %w", err)
	}

	picked, err := release.Pick(releases, w.assetName)
	if err == release.ErrNoRelease {
		w.log.Info("no qualifying release; nothing to do")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pick release: %w", err)
	}

	if picked.Release.Tag == current {
		return nil
	}

	w.log.Info("update available", "current", current, "available", picked.Release.Tag)

	busy, err := w.isBusy(ctx)
	if err != nil {
		return fmt.Errorf("check state: %w", err)
	}
	if busy {
		shouldDefer, waited := w.shouldDefer()
		if shouldDefer {
			w.log.Info("booth is busy; deferring update", "tag", picked.Release.Tag, "waited", waited.String(), "max", w.maxDefer.String())
			return nil
		}
		w.log.Info("booth has been busy past the defer limit; installing anyway", "tag", picked.Release.Tag)
	}

	assetBytes, digestHex, sigHex, err := w.download(ctx, picked.Release.Tag)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}

	if err := release.Verify(assetBytes, digestHex, sigHex); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "bykami-update-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	newBin := filepath.Join(tmpDir, w.assetName)
	if err := os.WriteFile(newBin, assetBytes, 0o755); err != nil {
		return fmt.Errorf("write new binary: %w", err)
	}

	if err := w.swapper.Swap(newBin); err != nil {
		return fmt.Errorf("swap: %w", err)
	}

	if err := w.swapper.Restart(); err != nil {
		w.log.Error("restart failed; rolling back", "err", err)
		if rerr := w.swapper.Rollback(); rerr != nil {
			w.log.Error("rollback failed", "err", rerr)
			return fmt.Errorf("restart failed and rollback failed: %w", rerr)
		}
		w.log.Info("rolled back to previous binary")
		// The old binary is back in place, but this process is still the new
		// one (the restart failed before the process was replaced). Restart
		// again to load the old binary. If this also fails, the booth stays on
		// the new (broken) binary until the next poll or a manual restart.
		if rerr := w.swapper.Restart(); rerr != nil {
			w.log.Error("restart after rollback also failed", "err", rerr)
		}
		return fmt.Errorf("restart failed: %w", err)
	}

	if err := w.waitHealthy(ctx); err != nil {
		w.log.Error("unhealthy after update; rolling back", "err", err)
		if rerr := w.swapper.Rollback(); rerr != nil {
			w.log.Error("rollback failed", "err", rerr)
			return fmt.Errorf("unhealthy and rollback failed: %w", rerr)
		}
		w.log.Info("rolled back to previous binary")
		// Restart so the old binary is actually loaded.
		if rerr := w.swapper.Restart(); rerr != nil {
			w.log.Error("restart after rollback failed", "err", rerr)
		}
		return fmt.Errorf("unhealthy after update: %w", err)
	}

	if err := os.WriteFile(w.versionFile, []byte(picked.Release.Tag), 0o644); err != nil {
		return fmt.Errorf("write version file: %w", err)
	}
	_ = os.Remove(w.deferFile)
	w.log.Info("healthy", "tag", picked.Release.Tag)
	return nil
}

func (w *Worker) currentVersion() (string, error) {
	b, err := os.ReadFile(w.versionFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (w *Worker) fetchList(ctx context.Context) ([]release.Release, error) {
	url := w.listURL
	if url == "" {
		url = fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100", w.repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	res, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
		return nil, fmt.Errorf("%s: %s", res.Status, body)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return release.ParseList(body)
}

func (w *Worker) isBusy(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.stateURL, nil)
	if err != nil {
		return true, err
	}
	res, err := w.client.Do(req)
	if err != nil {
		w.log.Warn("no answer from state endpoint; treating booth as busy", "url", w.stateURL, "err", err)
		return true, nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		w.log.Warn("state endpoint refused; treating booth as busy", "status", res.StatusCode)
		return true, nil
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return true, err
	}

	var state struct {
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		w.log.Warn("state body did not parse; treating booth as busy", "err", err)
		return true, nil
	}
	return len(state.Session) > 4 && string(state.Session) != "null", nil
}

func (w *Worker) shouldDefer() (bool, time.Duration) {
	now := time.Now().Unix()
	var since int64

	b, err := os.ReadFile(w.deferFile)
	if err != nil || len(b) == 0 {
		since = now
		_ = os.WriteFile(w.deferFile, []byte(strconv.FormatInt(since, 10)), 0o644)
	} else {
		since, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if since == 0 {
			since = now
			_ = os.WriteFile(w.deferFile, []byte(strconv.FormatInt(since, 10)), 0o644)
		}
	}

	waited := time.Since(time.Unix(since, 0))
	return waited < w.maxDefer, waited
}

func (w *Worker) waitHealthy(ctx context.Context) error {
	for i := range 15 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.stateURL, nil)
		if err != nil {
			return err
		}
		res, err := w.client.Do(req)
		if err == nil && res.StatusCode == http.StatusOK {
			res.Body.Close()
			return nil
		}
		if res != nil {
			res.Body.Close()
		}
		if i < 14 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return errors.New("state endpoint did not become healthy")
}

func (w *Worker) download(ctx context.Context, tag string) (asset []byte, digestHex, sigHex string, err error) {
	base := w.downloadBase
	if base == "" {
		base = fmt.Sprintf("https://github.com/%s/releases/download/%s", w.repo, tag)
	}

	asset, err = w.downloadURL(ctx, base+"/"+w.assetName)
	if err != nil {
		return nil, "", "", fmt.Errorf("asset: %w", err)
	}

	digestBytes, err := w.downloadURL(ctx, base+"/"+w.assetName+".sha256")
	if err != nil {
		return nil, "", "", fmt.Errorf("digest: %w", err)
	}
	fields := strings.Fields(string(digestBytes))
	if len(fields) == 0 {
		return nil, "", "", errors.New("empty digest file")
	}
	digestHex = fields[0]

	sigBytes, err := w.downloadURL(ctx, base+"/"+w.assetName+".sig")
	if err != nil {
		return nil, "", "", fmt.Errorf("signature: %w", err)
	}
	sigHex = strings.TrimSpace(string(sigBytes))

	return asset, digestHex, sigHex, nil
}

func (w *Worker) downloadURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 256<<20))
}
