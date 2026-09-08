// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// Store holds the ceiling and the per-tenant policies.
//
// The ceiling is the deployment's (measured image, or an owner-set file on
// the volume). A tenant's policy is THEIR data: D6' puts it on the acting
// user's Drive under their own keys, so an enterprise admin's access to it is
// decided by Drive's tenant roles rather than by anything the harness grants
// (D11), and the enclave keeps no per-user record. The store therefore reads
// and writes tenant documents through a TenantBackend the command layer
// supplies (the holder's granted Drive folder), caching them in memory for
// the life of the process. Without a backend, or before the holder has
// connected a Drive, a tenant policy lives in memory only and the write
// reports that it was not persisted — never silently.
type Store struct {
	dir string

	mu      sync.RWMutex
	ceiling *Document
	tenants map[string]*Document // keyed by subjectKey(sub)
	backend TenantBackend

	// OnCeilingChange fires after the ceiling's digest moves, so the caller can
	// re-stamp the attested extension and evict anything derived from it.
	OnCeilingChange func(*Document)
}

// TenantBackend is where one subject's policy document lives.
type TenantBackend interface {
	// Load returns the stored document, found=false when the subject has
	// none, and an error when the backend could not be consulted.
	Load(sub string) (raw []byte, found bool, err error)
	// Save stores the document. ErrNotPersisted means the backend has
	// nowhere to put it for this subject yet (no Drive connected); the
	// caller keeps the document in memory and says so.
	Save(sub string, raw []byte) error
}

// ErrNotPersisted is returned by a TenantBackend that cannot store for the
// subject yet. It is not a failure of the policy, only of its durability.
var ErrNotPersisted = errors.New("policy: not persisted (no Drive connected for this user)")

// NewStore opens (and creates) the policy directory.
func NewStore(dir string) *Store {
	return &Store{dir: dir, tenants: map[string]*Document{}}
}

// SetTenantBackend wires where tenant documents are kept.
func (s *Store) SetTenantBackend(b TenantBackend) {
	s.mu.Lock()
	s.backend = b
	s.mu.Unlock()
}

func (s *Store) ceilingPath() string { return filepath.Join(s.dir, "ceiling.json") }

// subjectKey keys the in-memory cache without holding a raw pairwise subject
// in any structure that might be dumped: it is an identifier for a person.
func subjectKey(sub string) string {
	sum := sha256.Sum256([]byte(sub))
	return hex.EncodeToString(sum[:16])
}

// LoadCeiling reads the ceiling from disk, falling back to a bootstrap
// document synthesised from the measured image environment when none has been
// written. A harness that predates the policy work therefore keeps behaving
// exactly as its image says instead of failing closed on an absent file.
// Resolution order:
//
//  1. An owner-set document on the encrypted volume. Reserved for the
//     enterprise shape, where admins set a ceiling without a rebuild.
//     ⚠ NOTHING WRITES THIS YET. The platform's configure surface is the
//     correct authorisation (owner/admin via authorizeConfigure), and
//     declaring a configure endpoint today is unsafe BOTH ways: marking it
//     required arms the freeze gate (503 until configured — an outage on a
//     running deployment), and marking it optional SKIPS the authz gate, a
//     known live defect. So the app's ceiling-write endpoint stays 501 and
//     this path only ever reads.
//  2. A document baked into the IMAGE. The right home for a Privasys-owned
//     harness: the ceiling is ours, and putting it in the image makes it
//     MEASURED rather than merely digest-attested — the code hash already
//     commits to it, so a verifier gets the stronger property for free.
//  3. A bootstrap from the image environment, so a deployment predating all of
//     this behaves exactly as its image says rather than failing closed.
func (s *Store) LoadCeiling(imagePath string, fallbackMode Mode, fallbackAllowlist []string, harnessAppID string) (*Document, error) {
	raw, err := os.ReadFile(s.ceilingPath())
	if errors.Is(err, os.ErrNotExist) && imagePath != "" {
		if baked, bakedErr := os.ReadFile(imagePath); bakedErr == nil {
			d, parseErr := Parse(baked, ScopeCeiling)
			if parseErr != nil {
				// A malformed baked ceiling is a BUILD error. Falling back to
				// the environment would silently ship a posture nobody wrote.
				return nil, fmt.Errorf("policy: image ceiling %s is unusable: %w", imagePath, parseErr)
			}
			s.setCeiling(d)
			log.Printf("[policy] ceiling loaded from the image, measured (mode=%s digest=%.16s…)",
				d.Egress.Mode, d.Digest())
			return d, nil
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		d := Bootstrap(fallbackMode, fallbackAllowlist, harnessAppID)
		s.setCeiling(d)
		log.Printf("[policy] no ceiling document; bootstrapped from the image environment (mode=%s digest=%.16s…)",
			d.Egress.Mode, d.Digest())
		return d, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy: read ceiling: %w", err)
	}
	d, err := Parse(raw, ScopeCeiling)
	if err != nil {
		// A malformed ceiling must NOT fall back to the permissive image
		// default: that would turn a corrupt file into a widening. Fail closed
		// and say so.
		return nil, fmt.Errorf("policy: ceiling on disk is unusable (refusing to fall back to a more permissive default): %w", err)
	}
	s.setCeiling(d)
	log.Printf("[policy] ceiling loaded (mode=%s digest=%.16s…)", d.Egress.Mode, d.Digest())
	return d, nil
}

func (s *Store) setCeiling(d *Document) {
	s.mu.Lock()
	prev := ""
	if s.ceiling != nil {
		prev = s.ceiling.Digest()
	}
	s.ceiling = d
	s.mu.Unlock()
	if prev != d.Digest() && s.OnCeilingChange != nil {
		s.OnCeilingChange(d)
	}
}

// Ceiling returns the active ceiling.
func (s *Store) Ceiling() *Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ceiling
}

// SaveCeiling validates and persists a new ceiling. The caller is responsible
// for authorising it — this is the app owner's tier, gated by the configure
// surface's owner/admin role.
func (s *Store) SaveCeiling(raw []byte) (*Document, error) {
	d, err := Parse(raw, ScopeCeiling)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(s.ceilingPath(), d.Raw()); err != nil {
		return nil, err
	}
	s.setCeiling(d)
	log.Printf("[policy] ceiling REPLACED (mode=%s digest=%.16s…)", d.Egress.Mode, d.Digest())
	return d, nil
}

// Tenant returns one subject's policy, or nil when they have set none. A read
// error is reported as "no policy", never as an error that would fail the
// request open — the ceiling still applies either way.
func (s *Store) Tenant(sub string) *Document {
	if sub == "" {
		return nil
	}
	key := subjectKey(sub)
	s.mu.RLock()
	d, ok := s.tenants[key]
	backend := s.backend
	s.mu.RUnlock()
	if ok {
		return d
	}
	if backend == nil {
		return nil
	}
	raw, found, err := backend.Load(sub)
	if err != nil {
		// A backend outage is not "no policy": say so, but the ceiling still
		// applies and the next read tries again.
		log.Printf("[policy] tenant document for %.8s… could not be read (the ceiling still applies): %v", sub, err)
		return nil
	}
	if !found {
		return nil
	}
	parsed, err := Parse(raw, ScopeTenant)
	if err != nil {
		log.Printf("[policy] tenant document for %.8s… is unusable, ignoring it (the ceiling still applies): %v", sub, err)
		return nil
	}
	s.mu.Lock()
	s.tenants[key] = parsed
	s.mu.Unlock()
	return parsed
}

// SaveTenant validates one subject's policy and persists it. persisted is
// false when the document was accepted but has no durable home yet (no
// Drive connected for this user): it applies for the life of the process
// and the caller must say so.
//
// The narrowing invariant is checked HERE, at the write, as well as being
// enforced at every request by conjunction. Rejecting a widening document when
// it is written gives the user an immediate, comprehensible error instead of a
// policy that silently does less than it says.
func (s *Store) SaveTenant(sub string, raw []byte) (d *Document, persisted bool, err error) {
	if sub == "" {
		return nil, false, errors.New("policy: no acting subject")
	}
	d, err = Parse(raw, ScopeTenant)
	if err != nil {
		return nil, false, err
	}
	if d.Subject != sub {
		return nil, false, fmt.Errorf("policy: document subject does not match the acting subject")
	}
	ceiling := s.Ceiling()
	if ceiling == nil {
		return nil, false, errors.New("policy: no ceiling loaded")
	}
	if d.Egress.Mode.rank() > ceiling.Egress.Mode.rank() {
		return nil, false, fmt.Errorf("policy: egress mode %q is more permissive than this harness permits (%q); a tenant policy may only narrow",
			d.Egress.Mode, ceiling.Egress.Mode)
	}
	// A tenant allowlist entry the ceiling would refuse is not an error — the
	// conjunction simply never admits it — but saying so early avoids a user
	// believing they enabled something they did not.
	if ceiling.Egress.Mode == ModeAllowlist {
		for _, h := range d.Egress.Allowlist {
			probe := h
			if trimmed, ok := probeHost(h); ok {
				probe = trimmed
			}
			if !HostMatches(probe, ceiling.Egress.Allowlist) {
				return nil, false, fmt.Errorf("policy: %q is not permitted by this harness's allowlist; a tenant policy may only narrow", h)
			}
		}
	}
	// Spend narrows too: a tenant may not consent to more per call, or more
	// per session, than the ceiling caps — an enterprise's cap is the
	// operator's decision. Where the ceiling sets no figure the tenant is
	// free, since the fee is the user's own money.
	if d.Spend != nil && ceiling.Spend != nil {
		if c := ceiling.Spend.PerCallMaxCredits; c > 0 && d.Spend.PerCallMaxCredits > c {
			return nil, false, fmt.Errorf("policy: per-call spending of %d credits exceeds this harness's cap of %d", d.Spend.PerCallMaxCredits, c)
		}
		if c := ceiling.Spend.PerSessionMaxCredits; c > 0 && (d.Spend.PerSessionMaxCredits == 0 || d.Spend.PerSessionMaxCredits > c) {
			return nil, false, fmt.Errorf("policy: per-session spending must stay within this harness's cap of %d credits", c)
		}
		for tool, v := range d.Spend.Tools {
			if c := ceiling.Spend.PerCall(tool); c > 0 && v > c {
				return nil, false, fmt.Errorf("policy: per-call spending for %q of %d credits exceeds this harness's cap of %d", tool, v, c)
			}
		}
	}
	s.mu.RLock()
	backend := s.backend
	s.mu.RUnlock()
	persisted = false
	if backend != nil {
		switch err := backend.Save(sub, d.Raw()); {
		case err == nil:
			persisted = true
		case errors.Is(err, ErrNotPersisted):
			// Accepted, applies now, kept in memory; the caller tells the user.
		default:
			return nil, false, err
		}
	}
	s.mu.Lock()
	s.tenants[subjectKey(sub)] = d
	s.mu.Unlock()
	log.Printf("[policy] tenant policy saved for %.8s… (mode=%s digest=%.16s… persisted=%v)", sub, d.Egress.Mode, d.Digest(), persisted)
	return d, persisted, nil
}

// probeHost turns a wildcard entry into a hostname the ceiling's matcher can
// test, so "*.acme.com" under a ceiling listing "*.acme.com" is admitted.
func probeHost(entry string) (string, bool) {
	if len(entry) > 2 && entry[0] == '*' && entry[1] == '.' {
		return entry[2:], true
	}
	return entry, false
}

// Effective resolves both tiers for one acting subject.
func (s *Store) Effective(sub string) Effective {
	return Effective{Ceiling: s.Ceiling(), Tenant: s.Tenant(sub)}
}

// writeFileAtomic writes through a temp file and renames, so a crash mid-write
// cannot leave a half-parsed policy that the loader would refuse on next boot.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
