// Command relsign creates detached ed25519 signatures for release assets.
//
// It reads the private signing seed from the environment variable
// RELEASE_SIGNING_KEY, which must be the base64 encoding of exactly 32 bytes.
// For each file path given as an argument it writes <path>.sig containing the
// hex-encoded signature over the file's SHA256 digest.
//
// Usage in CI:
//
//	RELEASE_SIGNING_KEY=${{ secrets.RELEASE_SIGNING_KEY }} relsign bykami-agent.exe bykami-agent-linux-amd64
//
// The public key is compiled into agent/internal/release and is:
//
//	ffb16edb4bc8f50c9afea383891f379d4040911def0165ac3e47ea348973b458
//
// Rotating the key means shipping a new agent binary first, because the old
// binary will not trust signatures made with the new key.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: relsign <asset> ...")
	}

	seedB64 := os.Getenv("RELEASE_SIGNING_KEY")
	if seedB64 == "" {
		return fmt.Errorf("RELEASE_SIGNING_KEY is not set")
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil {
		return fmt.Errorf("RELEASE_SIGNING_KEY is not valid base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("RELEASE_SIGNING_KEY decoded to %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		digest := sha256.Sum256(data)
		sig := ed25519.Sign(priv, digest[:])

		out := path + ".sig"
		if err := os.WriteFile(out, []byte(hex.EncodeToString(sig)), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		fmt.Println(out)
	}
	return nil
}
