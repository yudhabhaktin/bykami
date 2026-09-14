package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

// testKey generates a fresh ed25519 key pair for each test so tests cannot
// interfere with each other. The real PublicKey is compiled in; these tests
// temporarily replace it.
func testKey(t *testing.T) (pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv.Public().(ed25519.PublicKey), priv
}

func signAsset(t *testing.T, priv ed25519.PrivateKey, asset []byte) string {
	t.Helper()
	digest := sha256.Sum256(asset)
	sig := ed25519.Sign(priv, digest[:])
	return hex.EncodeToString(sig)
}

func TestPickReturnsNewestAgentRelease(t *testing.T) {
	got, err := Pick([]Release{
		{Tag: "agent-v2", PublishedAt: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), Assets: []Asset{{Name: "a.exe"}}},
		{Tag: "agent-v1", PublishedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Assets: []Asset{{Name: "a.exe"}}},
	}, "a.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got.Release.Tag != "agent-v2" {
		t.Errorf("tag = %q, want agent-v2", got.Release.Tag)
	}
}

// GitHub's list endpoint does not reliably return releases newest-first, so
// Pick must sort explicitly. A release published later but listed earlier must
// not win.
func TestPickSortsByPublishedAtNotByInputOrder(t *testing.T) {
	got, err := Pick([]Release{
		{Tag: "agent-older", PublishedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Assets: []Asset{{Name: "a.exe"}}},
		{Tag: "agent-newer", PublishedAt: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), Assets: []Asset{{Name: "a.exe"}}},
	}, "a.exe")
	if err != nil {
		t.Fatal(err)
	}
	if got.Release.Tag != "agent-newer" {
		t.Errorf("tag = %q, want agent-newer", got.Release.Tag)
	}
}

func TestPickSkipsDraftsAndPrereleases(t *testing.T) {
	_, err := Pick([]Release{
		{Tag: "agent-draft", Draft: true, PublishedAt: time.Now(), Assets: []Asset{{Name: "a.exe"}}},
		{Tag: "agent-pre", Prerelease: true, PublishedAt: time.Now(), Assets: []Asset{{Name: "a.exe"}}},
	}, "a.exe")
	if err == nil {
		t.Fatal("expected no qualifying release")
	}
}

func TestPickSkipsApiReleases(t *testing.T) {
	_, err := Pick([]Release{
		{Tag: "api-v1", PublishedAt: time.Now(), Assets: []Asset{{Name: "a.exe"}}},
	}, "a.exe")
	if err == nil {
		t.Fatal("expected no qualifying release")
	}
}

// A release without the requested asset is skipped rather than treated as an
// error. This is what makes the poller safe to enable before the first release
// that carries a given platform's binary.
func TestPickSkipsReleaseWithoutAsset(t *testing.T) {
	_, err := Pick([]Release{
		{Tag: "agent-v1", PublishedAt: time.Now(), Assets: []Asset{{Name: "other.exe"}}},
	}, "a.exe")
	if err == nil {
		t.Fatal("expected no qualifying release")
	}
}

func TestVerifyHappyPath(t *testing.T) {
	pub, priv := testKey(t)
	old := PublicKey
	PublicKey = pub
	defer func() { PublicKey = old }()

	asset := []byte("good binary")
	digest := sha256.Sum256(asset)
	sig := signAsset(t, priv, asset)

	if err := Verify(asset, hex.EncodeToString(digest[:]), sig); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerifyTamperedAssetFails(t *testing.T) {
	pub, priv := testKey(t)
	old := PublicKey
	PublicKey = pub
	defer func() { PublicKey = old }()

	asset := []byte("good binary")
	digest := sha256.Sum256(asset)
	sig := signAsset(t, priv, asset)

	asset[0] ^= 0xff
	if err := Verify(asset, hex.EncodeToString(digest[:]), sig); err == nil {
		t.Fatal("expected error for tampered asset")
	}
}

func TestVerifyWrongSignatureFails(t *testing.T) {
	pub, priv := testKey(t)
	old := PublicKey
	PublicKey = pub
	defer func() { PublicKey = old }()

	asset := []byte("good binary")
	digest := sha256.Sum256(asset)
	_ = signAsset(t, priv, asset)

	badSig := hex.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := Verify(asset, hex.EncodeToString(digest[:]), badSig); err == nil {
		t.Fatal("expected error for wrong signature")
	}
}

func TestParseListHandlesGitHubJSON(t *testing.T) {
	body := []byte(`[
		{
			"tag_name": "agent-v1",
			"published_at": "2024-01-02T00:00:00Z",
			"draft": false,
			"prerelease": false,
			"assets": [{"name": "a.exe", "browser_download_url": "https://example.com/a.exe"}]
		},
		{
			"tag_name": "api-v1",
			"published_at": "2024-01-03T00:00:00Z",
			"draft": false,
			"prerelease": false,
			"assets": [{"name": "a.exe", "browser_download_url": "https://example.com/a.exe"}]
		}
	]`)
	rs, err := ParseList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 {
		t.Fatalf("len = %d, want 2", len(rs))
	}
	if rs[0].Tag != "agent-v1" {
		t.Errorf("tag = %q, want agent-v1", rs[0].Tag)
	}
	if rs[0].Assets[0].Name != "a.exe" {
		t.Errorf("asset = %q, want a.exe", rs[0].Assets[0].Name)
	}
}

func TestParseListFallsBackToCreatedAt(t *testing.T) {
	body := []byte(`[{
		"tag_name": "agent-v1",
		"created_at": "2024-01-02T00:00:00Z",
		"draft": false,
		"prerelease": false,
		"assets": []
	}]`)
	rs, err := ParseList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("len = %d, want 1", len(rs))
	}
	want := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if !rs[0].PublishedAt.Equal(want) {
		t.Errorf("published_at = %v, want %v", rs[0].PublishedAt, want)
	}
}
