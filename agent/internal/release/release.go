// Package release knows how to read a GitHub releases list, pick the newest
// qualifying agent release, and verify an asset before it is installed.
//
// Nothing here makes network calls: the caller fetches the list and the asset
// bytes, and this package decides which release to use and whether the bytes
// are trustworthy.
package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// PublicKey is the ed25519 public key releases are signed with. Compiled in,
// so rotating it means shipping a new binary — the old binary will not trust
// signatures made with the new key, and the new binary will not trust
// signatures made with the old one. The key pair is generated offline; the
// private seed lives in the GitHub environment secret RELEASE_SIGNING_KEY.
//
// Hex: ffb16edb4bc8f50c9afea383891f379d4040911def0165ac3e47ea348973b458
var PublicKey = mustHex("ffb16edb4bc8f50c9afea383891f379d4040911def0165ac3e47ea348973b458")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// Release is one GitHub release, trimmed to the fields this package needs.
type Release struct {
	Tag         string
	PublishedAt time.Time
	Draft       bool
	Prerelease  bool
	Assets      []Asset
}

// Asset is one file attached to a release.
type Asset struct {
	Name string
	URL  string
}

// PickResult is what Pick returns: the chosen release and the asset within it.
type PickResult struct {
	Release Release
	Asset   Asset
}

// ErrNoRelease is returned when no release qualifies.
var ErrNoRelease = errors.New("release: no qualifying release")

// ParseList converts the GitHub API releases list JSON into Release values.
func ParseList(body []byte) ([]Release, error) {
	var raw []struct {
		TagName    string `json:"tag_name"`
		Published  string `json:"published_at"`
		Created    string `json:"created_at"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("release: parse list: %w", err)
	}

	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		pub, err := time.Parse(time.RFC3339, r.Published)
		if err != nil {
			// Fallback to created_at, which is always present.
			pub, err = time.Parse(time.RFC3339, r.Created)
			if err != nil {
				continue
			}
		}
		assets := make([]Asset, 0, len(r.Assets))
		for _, a := range r.Assets {
			assets = append(assets, Asset{Name: a.Name, URL: a.URL})
		}
		out = append(out, Release{
			Tag:         r.TagName,
			PublishedAt: pub,
			Draft:       r.Draft,
			Prerelease:  r.Prerelease,
			Assets:      assets,
		})
	}
	return out, nil
}

// Pick returns the newest release that qualifies: tag starts with "agent-",
// not draft, not prerelease, and carries an asset with the given name.
//
// Sorting is explicit rather than trusting GitHub's order, which is not
// guaranteed: the list endpoint has returned releases out of order on this
// repository — an api-* release published a minute after an agent-* one was
// returned ahead of it — so taking the first match would install a stale
// binary or appear to sit on the current version forever.
func Pick(releases []Release, assetName string) (PickResult, error) {
	candidates := make([]Release, 0, len(releases))
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		if !hasPrefix(r.Tag, "agent-") {
			continue
		}
		if !hasAsset(r, assetName) {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return PickResult{}, ErrNoRelease
	}

	// Explicit sort: GitHub does not guarantee newest-first.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].PublishedAt.After(candidates[j].PublishedAt)
	})

	chosen := candidates[0]
	var chosenAsset Asset
	for _, a := range chosen.Assets {
		if a.Name == assetName {
			chosenAsset = a
			break
		}
	}
	return PickResult{Release: chosen, Asset: chosenAsset}, nil
}

// Verify checks an asset against its SHA256 digest and an ed25519 detached
// signature. The signature is hex-encoded, in a file named <asset>.sig, and
// signs the SHA256 digest of the asset bytes (not the raw bytes).
//
// digest is the expected SHA256 hex string (from the .sha256 file).
// sigHex is the hex-encoded signature (from the .sig file).
func Verify(assetBytes []byte, digestHex, sigHex string) error {
	got := sha256.Sum256(assetBytes)
	want, err := hex.DecodeString(digestHex)
	if err != nil {
		return fmt.Errorf("release: bad digest hex: %w", err)
	}
	if !equal(got[:], want) {
		return errors.New("release: digest mismatch")
	}

	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("release: bad signature hex: %w", err)
	}
	if len(PublicKey) != ed25519.PublicKeySize {
		return errors.New("release: compiled-in public key has wrong length")
	}
	if !ed25519.Verify(PublicKey, got[:], sig) {
		return errors.New("release: signature verification failed")
	}
	return nil
}

func hasAsset(r Release, name string) bool {
	for _, a := range r.Assets {
		if a.Name == name {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
