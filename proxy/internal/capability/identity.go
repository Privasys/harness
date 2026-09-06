// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

// Package capability implements the requesting-app half of the wallet's
// resource-capability protocol (see .operations/plans/wallet-resource-capabilities.md
// and harness-drive-grant-wallet.md).
//
// The harness asks the holder for scoped, revocable authority over a resource
// they own — in the first instance, one folder of their personal Drive, so
// their sessions persist under their own keys instead of inside this enclave.
package capability

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Identity is the harness's signing identity for capability grants: the key
// whose public half the holder binds when they approve, and whose private half
// mints the tokens that exercise the grant afterwards.
//
// Why a key of our own rather than the RA-TLS leaf. The natural instinct is to
// bind the serving certificate's key, since the wallet has just attested it —
// but it cannot work: the leaf is ECDSA (P-256) where the grant binding is
// Ed25519, and its private half belongs to the runtime, not to this workload,
// so nothing in this process could ever sign with it. Instead we generate our
// own Ed25519 pair and return the public half INSIDE the attested response the
// wallet fetches. That preserves the property the rule exists for: the key the
// holder binds was proved over the same attested channel that established the
// identity shown on their screen.
//
// The private half is sealed: it lives only on the enclave's encrypted volume
// and is never transmitted, logged, or exposed by any endpoint.
type Identity struct {
	mu   sync.RWMutex
	priv ed25519.PrivateKey
	path string
}

// NewIdentity loads the sealed key, generating one on first use.
func NewIdentity(dir string) (*Identity, error) {
	id := &Identity{path: filepath.Join(dir, "capability-key.bin")}
	raw, err := os.ReadFile(id.path)
	switch {
	case err == nil:
		if len(raw) != ed25519.PrivateKeySize {
			// A truncated or foreign file must NOT be silently replaced: doing
			// so would mint a new public key and quietly invalidate every
			// capability the holder has already approved, with no signal
			// beyond writes starting to fail. Refuse and say what happened.
			return nil, fmt.Errorf("capability: sealed key at %s is %d bytes, expected %d; "+
				"refusing to replace it (that would invalidate every approved capability silently)",
				id.path, len(raw), ed25519.PrivateKeySize)
		}
		id.priv = ed25519.PrivateKey(raw)
		log.Printf("[capability] sealed signing identity loaded (%s)", id.PublicKeyB64()[:16]+"…")
		return id, nil
	case errors.Is(err, os.ErrNotExist):
		_, priv, genErr := ed25519.GenerateKey(nil)
		if genErr != nil {
			return nil, fmt.Errorf("capability: generate key: %w", genErr)
		}
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return nil, mkErr
		}
		// 0600 on the encrypted volume. Written through a temp file so a crash
		// mid-write cannot leave a half key that the branch above would then
		// refuse to load on every subsequent boot.
		tmp := id.path + ".tmp"
		if wErr := os.WriteFile(tmp, priv, 0o600); wErr != nil {
			return nil, wErr
		}
		if rErr := os.Rename(tmp, id.path); rErr != nil {
			return nil, rErr
		}
		id.priv = priv
		log.Printf("[capability] sealed signing identity generated (%s)", id.PublicKeyB64()[:16]+"…")
		return id, nil
	default:
		return nil, fmt.Errorf("capability: read sealed key: %w", err)
	}
}

// PublicKeyB64 is the base64 Ed25519 public key the holder binds. This is the
// only half that ever leaves the enclave.
func (i *Identity) PublicKeyB64() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.priv == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(i.priv.Public().(ed25519.PublicKey))
}

// Sign signs a token payload with the sealed key.
func (i *Identity) Sign(payload []byte) ([]byte, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if i.priv == nil {
		return nil, errors.New("capability: no signing identity")
	}
	return ed25519.Sign(i.priv, payload), nil
}

// sha256sum is used to key stored records by subject without writing the
// subject itself to the filesystem.
func sha256sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
