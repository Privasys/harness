// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kind and permission vocabularies, mirroring the wallet's closed sets. The
// wallet renders only what it knows and refuses the rest, so a value invented
// here would simply fail on the holder's screen.
const (
	KindStorageFolder = "storage.folder"
	PermRead          = "read"
	PermWrite         = "write"
)

// pendingTTL bounds how long an unanswered request stays fetchable. A nonce is
// the only thing standing between "the holder was pushed" and "someone fetched
// the request", so it must not be valid indefinitely.
const pendingTTL = 15 * time.Minute

// Ask is the capability being requested, as the wallet will render it.
type Ask struct {
	Kind          string   `json:"kind"`
	Permissions   []string `json:"permissions"`
	ResourceLabel string   `json:"resource_label"`
	// Request is opaque to the wallet and forwarded verbatim to the resource
	// service. It must NOT name an ownership boundary — Drive refuses any
	// request naming a tenant, at any depth, and that refusal is what makes
	// the flow safe rather than merely tidy: the boundary is derived from the
	// authenticated holder, never suggested by the app asking for access.
	Request json.RawMessage `json:"request"`
}

// Pending is what the wallet fetches over RA-TLS. Everything that matters is
// learned here rather than from the push, which carries only the nonce and the
// host: a key that rode the push could be anyone's while the identity on the
// holder's screen was genuine.
type Pending struct {
	Nonce         string `json:"nonce"`
	BindingPubkey string `json:"binding_pubkey"`
	// ResourceApp names the resource service by IDENTITY, so the wallet
	// resolves it rather than following a URL out of this payload.
	ResourceApp string `json:"resource_app"`
	Capability  Ask    `json:"capability"`

	// Subject is the acting user this request is for. Never serialised to the
	// wallet — it is our own bookkeeping, so an approved capability is filed
	// against the right person.
	Subject string    `json:"-"`
	Created time.Time `json:"-"`
}

// Granted is what the resource service produced, as relayed by the wallet.
type Granted struct {
	CapabilityID  string            `json:"capability_id"`
	Status        string            `json:"status"`
	ServiceResult map[string]string `json:"service_result,omitempty"`
	At            time.Time         `json:"at"`
}

// Usable reports whether this record can address a resource. Drive returns the
// coordinates in service_result; without them there is nothing to address.
func (g *Granted) Usable() bool {
	return g != nil && g.Status == "approved" && g.CapabilityID != "" &&
		g.ServiceResult["tenant_id"] != "" && g.ServiceResult["node_id"] != ""
}

// Store holds outstanding requests and the capabilities that came back.
//
// ⚠ Transitional location, exactly as the policy store is: capabilities live
// on the enclave's encrypted volume keyed by subject. That is correct while
// one container serves one user and the container IS the tenancy boundary. It
// must not outlive that.
type Store struct {
	mu       sync.RWMutex
	pending  map[string]*Pending // by nonce
	granted  map[string]*Granted // by subject
	dir      string
	identity *Identity
}

func NewStore(dir string, id *Identity) *Store {
	s := &Store{
		pending:  map[string]*Pending{},
		granted:  map[string]*Granted{},
		dir:      dir,
		identity: id,
	}
	s.load()
	return s
}

func (s *Store) grantPath(subject string) string {
	return filepath.Join(s.dir, "capabilities", subjectKey(subject)+".json")
}

// Create registers a request for one subject and returns it. The nonce is the
// only secret in the flow before the wallet's attested fetch, so it comes from
// crypto/rand and is long enough not to be guessable.
func (s *Store) Create(subject, resourceApp, folder, label string, perms []string) (*Pending, error) {
	if subject == "" {
		return nil, errors.New("capability: no acting subject")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	req, err := json.Marshal(map[string]string{"folder": folder})
	if err != nil {
		return nil, err
	}
	p := &Pending{
		Nonce:         base64.RawURLEncoding.EncodeToString(raw),
		BindingPubkey: s.identity.PublicKeyB64(),
		ResourceApp:   resourceApp,
		Capability: Ask{
			Kind:          KindStorageFolder,
			Permissions:   perms,
			ResourceLabel: label,
			Request:       req,
		},
		Subject: subject,
		Created: time.Now().UTC(),
	}
	s.mu.Lock()
	s.sweepLocked()
	s.pending[p.Nonce] = p
	s.mu.Unlock()
	log.Printf("[capability] request created for %.8s… (%s, %v)", subject, folder, perms)
	return p, nil
}

// Get returns an unexpired pending request by nonce.
func (s *Store) Get(nonce string) (*Pending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	p, ok := s.pending[nonce]
	return p, ok
}

func (s *Store) sweepLocked() {
	cut := time.Now().UTC().Add(-pendingTTL)
	for n, p := range s.pending {
		if p.Created.Before(cut) {
			delete(s.pending, n)
		}
	}
}

// Resolve records the outcome the wallet delivered and consumes the nonce.
//
// A nonce is single-use: replaying an outcome must not be able to overwrite a
// later, narrower capability with an earlier one.
func (s *Store) Resolve(nonce, status, capabilityID string, result map[string]string) (*Granted, error) {
	s.mu.Lock()
	p, ok := s.pending[nonce]
	if !ok {
		s.mu.Unlock()
		return nil, errors.New("capability: unknown or expired request")
	}
	delete(s.pending, nonce)
	g := &Granted{
		CapabilityID:  capabilityID,
		Status:        status,
		ServiceResult: result,
		At:            time.Now().UTC(),
	}
	subject := p.Subject
	if status == "approved" {
		s.granted[subject] = g
	}
	s.mu.Unlock()

	if status == "approved" {
		if err := s.persist(subject, g); err != nil {
			// Non-fatal: the capability works for this process either way, and
			// failing the wallet's delivery would leave the holder believing
			// their approval was lost when it was not.
			log.Printf("[capability] could not persist the capability for %.8s… (it holds for this run): %v", subject, err)
		}
		log.Printf("[capability] APPROVED for %.8s… (id=%s tenant=%s node=%s)",
			subject, capabilityID, result["tenant_id"], result["node_id"])
	} else {
		log.Printf("[capability] denied for %.8s…", subject)
	}
	return g, nil
}

// Granted returns the capability held for one subject, if any.
func (s *Store) Granted(subject string) *Granted {
	s.mu.RLock()
	g, ok := s.granted[subject]
	s.mu.RUnlock()
	if ok {
		return g
	}
	raw, err := os.ReadFile(s.grantPath(subject))
	if err != nil {
		return nil
	}
	var loaded Granted
	if json.Unmarshal(raw, &loaded) != nil {
		return nil
	}
	s.mu.Lock()
	s.granted[subject] = &loaded
	s.mu.Unlock()
	return &loaded
}

func (s *Store) persist(subject string, g *Granted) error {
	if err := os.MkdirAll(filepath.Dir(s.grantPath(subject)), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	tmp := s.grantPath(subject) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.grantPath(subject))
}

func (s *Store) load() {
	entries, err := os.ReadDir(filepath.Join(s.dir, "capabilities"))
	if err != nil {
		return
	}
	log.Printf("[capability] %d stored capability record(s) on the volume", len(entries))
}

func subjectKey(sub string) string {
	// Same reasoning as the policy store: a pairwise subject identifies a
	// person, and a directory listing should not enumerate them.
	return fmt.Sprintf("%x", sha256sum(sub))[:32]
}
