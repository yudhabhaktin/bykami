package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/release"
)

type mockSwapper struct {
	swapped      bool
	restarted    bool
	restartCount int
	rolledBack   bool
	newPath      string
	restartErr   error
}

func (m *mockSwapper) Swap(verifiedNew string) error {
	m.swapped = true
	m.newPath = verifiedNew
	return nil
}

func (m *mockSwapper) Restart() error {
	m.restarted = true
	m.restartCount++
	return m.restartErr
}

func (m *mockSwapper) Rollback() error {
	m.rolledBack = true
	return nil
}

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv.Public().(ed25519.PublicKey), priv
}

func signAsset(priv ed25519.PrivateKey, asset []byte) string {
	digest := sha256.Sum256(asset)
	sig := ed25519.Sign(priv, digest[:])
	return hex.EncodeToString(sig)
}

func makeReleaseServer(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, asset []byte, tag string) *httptest.Server {
	t.Helper()
	digest := sha256.Sum256(asset)
	sig := signAsset(priv, asset)
	name := "bykami-agent-linux-amd64"

	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name":     tag,
			"published_at": time.Now().Format(time.RFC3339),
			"draft":        false,
			"prerelease":   false,
			"assets": []map[string]string{{
				"name":                 name,
				"browser_download_url": "/" + name,
			}},
		}})
	})
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		w.Write(asset)
	})
	mux.HandleFunc("/"+name+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	})
	mux.HandleFunc("/"+name+".sig", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sig))
	})
	return httptest.NewServer(mux)
}

func TestPollSameVersionDoesNothing(t *testing.T) {
	pub, priv := testKey(t)
	oldKey := release.PublicKey
	release.PublicKey = pub
	defer func() { release.PublicKey = oldKey }()

	asset := []byte("binary")
	releaseSrv := makeReleaseServer(t, pub, priv, asset, "agent-v1")
	defer releaseSrv.Close()

	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"session":null}`))
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	_ = os.WriteFile(vf, []byte("agent-v1"), 0o644)

	ms := &mockSwapper{}
	w := &Worker{
		repo:         "owner/repo",
		assetName:    "bykami-agent-linux-amd64",
		versionFile:  vf,
		stateURL:     state.URL,
		listURL:      releaseSrv.URL + "/releases",
		downloadBase: releaseSrv.URL,
		swapper:      ms,
		log:          testLog(t),
		client:       &http.Client{Timeout: 5 * time.Second},
	}

	if err := w.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if ms.swapped {
		t.Fatal("expected no swap when version matches")
	}
}

func TestPollMismatchedVersionAttemptsUpdate(t *testing.T) {
	pub, priv := testKey(t)
	oldKey := release.PublicKey
	release.PublicKey = pub
	defer func() { release.PublicKey = oldKey }()

	asset := []byte("binary")
	releaseSrv := makeReleaseServer(t, pub, priv, asset, "agent-v2")
	defer releaseSrv.Close()

	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"session":null}`))
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	_ = os.WriteFile(vf, []byte("agent-v1"), 0o644)

	ms := &mockSwapper{}
	w := &Worker{
		repo:         "owner/repo",
		assetName:    "bykami-agent-linux-amd64",
		versionFile:  vf,
		stateURL:     state.URL,
		listURL:      releaseSrv.URL + "/releases",
		downloadBase: releaseSrv.URL,
		swapper:      ms,
		log:          testLog(t),
		client:       &http.Client{Timeout: 5 * time.Second},
	}

	if err := w.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !ms.swapped {
		t.Fatal("expected swap when version mismatches")
	}
	if !ms.restarted {
		t.Fatal("expected restart after swap")
	}
	if ms.rolledBack {
		t.Fatal("unexpected rollback")
	}

	got, _ := os.ReadFile(vf)
	if string(got) != "agent-v2" {
		t.Fatalf("version file = %q, want agent-v2", got)
	}
}

func TestPollTamperedAssetNeverReachesSwapper(t *testing.T) {
	pub, priv := testKey(t)
	oldKey := release.PublicKey
	release.PublicKey = pub
	defer func() { release.PublicKey = oldKey }()

	asset := []byte("good binary")
	badAsset := []byte("bad binary")
	digest := sha256.Sum256(asset)
	sig := signAsset(priv, asset)

	name := "bykami-agent-linux-amd64"
	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name":     "agent-v2",
			"published_at": time.Now().Format(time.RFC3339),
			"draft":        false,
			"prerelease":   false,
			"assets": []map[string]string{{
				"name":                 name,
				"browser_download_url": "/" + name,
			}},
		}})
	})
	mux.HandleFunc("/"+name, func(w http.ResponseWriter, r *http.Request) {
		w.Write(badAsset)
	})
	mux.HandleFunc("/"+name+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	})
	mux.HandleFunc("/"+name+".sig", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sig))
	})
	releaseSrv := httptest.NewServer(mux)
	defer releaseSrv.Close()

	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"session":null}`))
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	_ = os.WriteFile(vf, []byte("agent-v1"), 0o644)

	ms := &mockSwapper{}
	w := &Worker{
		repo:         "owner/repo",
		assetName:    "bykami-agent-linux-amd64",
		versionFile:  vf,
		stateURL:     state.URL,
		listURL:      releaseSrv.URL + "/releases",
		downloadBase: releaseSrv.URL,
		swapper:      ms,
		log:          testLog(t),
		client:       &http.Client{Timeout: 5 * time.Second},
	}

	if err := w.Poll(context.Background()); err == nil {
		t.Fatal("expected error for tampered asset")
	}
	if ms.swapped {
		t.Fatal("tampered asset must not reach swapper")
	}
}

func TestPollFailedRestartRollsBack(t *testing.T) {
	pub, priv := testKey(t)
	oldKey := release.PublicKey
	release.PublicKey = pub
	defer func() { release.PublicKey = oldKey }()

	asset := []byte("binary")
	releaseSrv := makeReleaseServer(t, pub, priv, asset, "agent-v2")
	defer releaseSrv.Close()

	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"session":null}`))
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	_ = os.WriteFile(vf, []byte("agent-v1"), 0o644)

	ms := &mockSwapper{restartErr: fmt.Errorf("service refused")}
	w := &Worker{
		repo:         "owner/repo",
		assetName:    "bykami-agent-linux-amd64",
		versionFile:  vf,
		stateURL:     state.URL,
		listURL:      releaseSrv.URL + "/releases",
		downloadBase: releaseSrv.URL,
		swapper:      ms,
		log:          testLog(t),
		client:       &http.Client{Timeout: 5 * time.Second},
	}

	if err := w.Poll(context.Background()); err == nil {
		t.Fatal("expected error when restart fails")
	}
	if !ms.swapped {
		t.Fatal("expected swap")
	}
	if !ms.restarted {
		t.Fatal("expected restart attempt")
	}
	if ms.restartCount != 2 {
		t.Fatalf("expected 2 restarts (failed + after rollback), got %d", ms.restartCount)
	}
	if !ms.rolledBack {
		t.Fatal("expected rollback after failed restart")
	}

	got, _ := os.ReadFile(vf)
	if string(got) != "agent-v1" {
		t.Fatalf("version file = %q, want agent-v1 (must not be updated on failed restart)", got)
	}
}

func TestPollUnhealthyRollsBack(t *testing.T) {
	pub, priv := testKey(t)
	oldKey := release.PublicKey
	release.PublicKey = pub
	defer func() { release.PublicKey = oldKey }()

	asset := []byte("binary")
	releaseSrv := makeReleaseServer(t, pub, priv, asset, "agent-v2")
	defer releaseSrv.Close()

	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	_ = os.WriteFile(vf, []byte("agent-v1"), 0o644)

	ms := &mockSwapper{}
	w := &Worker{
		repo:         "owner/repo",
		assetName:    "bykami-agent-linux-amd64",
		versionFile:  vf,
		stateURL:     state.URL,
		listURL:      releaseSrv.URL + "/releases",
		downloadBase: releaseSrv.URL,
		swapper:      ms,
		log:          testLog(t),
		client:       &http.Client{Timeout: 5 * time.Second},
	}

	if err := w.Poll(context.Background()); err == nil {
		t.Fatal("expected error when health check fails")
	}
	if !ms.swapped {
		t.Fatal("expected swap")
	}
	if !ms.restarted {
		t.Fatal("expected restart")
	}
	if ms.restartCount != 2 {
		t.Fatalf("expected 2 restarts (initial + after rollback), got %d", ms.restartCount)
	}
	if !ms.rolledBack {
		t.Fatal("expected rollback after unhealthy")
	}

	got, _ := os.ReadFile(vf)
	if string(got) != "agent-v1" {
		t.Fatalf("version file = %q, want agent-v1 (must not be updated on unhealthy)", got)
	}
}

func TestPollBusyDefers(t *testing.T) {
	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"session":{"id":"sess-1"}}`))
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	deferFile := vf + ".defer-since"

	w := &Worker{
		repo:        "owner/repo",
		assetName:   "bykami-agent-linux-amd64",
		versionFile: vf,
		deferFile:   deferFile,
		maxDefer:    60 * time.Minute,
		stateURL:    state.URL,
		swapper:     &mockSwapper{},
		log:         testLog(t),
		client:      &http.Client{Timeout: 5 * time.Second},
	}

	busy, err := w.isBusy(context.Background())
	if err != nil {
		t.Fatalf("isBusy: %v", err)
	}
	if !busy {
		t.Fatal("expected busy")
	}

	shouldDefer, _ := w.shouldDefer()
	if !shouldDefer {
		t.Fatal("expected defer on first busy check")
	}
	if _, err := os.Stat(deferFile); err != nil {
		t.Fatalf("defer file not written: %v", err)
	}
}

func TestPollNoAnswerDefers(t *testing.T) {
	w := &Worker{
		repo:        "owner/repo",
		assetName:   "bykami-agent-linux-amd64",
		versionFile: filepath.Join(t.TempDir(), "version"),
		maxDefer:    time.Hour,
		stateURL:    "http://127.0.0.1:1/api/state",
		swapper:     &mockSwapper{},
		log:         testLog(t),
		client:      &http.Client{Timeout: time.Second},
	}

	busy, err := w.isBusy(context.Background())
	if err != nil {
		t.Fatalf("isBusy: %v", err)
	}
	if !busy {
		t.Fatal("expected busy when state endpoint does not answer")
	}
}

func TestPollPastBoundInstallsAnyway(t *testing.T) {
	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"session":{"id":"sess-1"}}`))
	}))
	defer state.Close()

	dir := t.TempDir()
	vf := filepath.Join(dir, "version")
	deferFile := vf + ".defer-since"
	_ = os.WriteFile(deferFile, []byte(fmt.Sprintf("%d", time.Now().Add(-2*time.Hour).Unix())), 0o644)

	w := &Worker{
		repo:        "owner/repo",
		assetName:   "bykami-agent-linux-amd64",
		versionFile: vf,
		deferFile:   deferFile,
		maxDefer:    60 * time.Minute,
		stateURL:    state.URL,
		swapper:     &mockSwapper{},
		log:         testLog(t),
		client:      &http.Client{Timeout: 5 * time.Second},
	}

	shouldDefer, waited := w.shouldDefer()
	if shouldDefer {
		t.Fatalf("expected install anyway after defer limit, but still deferring after %v", waited)
	}
}

func TestWaitHealthySucceeds(t *testing.T) {
	calls := 0
	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer state.Close()

	w := &Worker{
		stateURL: state.URL,
		client:   &http.Client{Timeout: 5 * time.Second},
		log:      testLog(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := w.waitHealthy(ctx); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	if calls < 3 {
		t.Fatalf("only %d calls, expected at least 3", calls)
	}
}

func TestWaitHealthyFails(t *testing.T) {
	state := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer state.Close()

	w := &Worker{
		stateURL: state.URL,
		client:   &http.Client{Timeout: 5 * time.Second},
		log:      testLog(t),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := w.waitHealthy(ctx); err == nil {
		t.Fatal("expected error from waitHealthy")
	}
}

func testLog(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
