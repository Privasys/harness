// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// Mirroring the session store into the holder's approved Drive folder.
//
// WHY A MIRROR RATHER THAN A DRIVE-NATIVE SESSION PROVIDER. dsh's session
// persistence is an interface with one careful implementation: JSONL with
// single-writer ownership, torn-tail recovery and opaque revision tokens.
// Replacing it with a Drive-native provider means reimplementing those
// semantics against a remote store with different failure modes — the kind of
// rewrite whose bugs are silent data loss. Instead the local JSONL store stays
// exactly as upstream wrote it, on the encrypted volume, and this mirrors it
// out. dsh keeps its correctness; the holder gets their data in their Drive,
// under their keys, where it survives this enclave entirely.
//
// The honest cost: a copy remains on /data. That is a CACHE, not the record —
// and while a harness is single-user, with the container as the tenancy
// boundary, it is the same volume the workspace already lives on. When per-user
// workers arrive that cache becomes per-user too, or goes away in favour of a
// true provider. D6' is satisfied in what matters — the durable home of a
// user's transcripts is their Drive.
//
// Uploads are content-addressed: a file is sent only when its bytes change, so
// an idle harness makes no Drive traffic and a busy one sends each session's
// log once per change rather than once per tick.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Syncer mirrors a local session root into one holder's Drive folder.
type Syncer struct {
	store     *Store
	identity  *Identity
	client    *http.Client
	driveHost string
	localRoot string

	mu sync.Mutex
	// uploaded maps a relative path to the content hash last stored, so an
	// unchanged file is never re-sent.
	uploaded map[string]string
	// folders caches path -> Drive node id, so a busy session does not re-list
	// the same folders on every tick.
	folders map[string]string
	last    time.Time
	lastErr string
	files   int
}

func NewSyncer(store *Store, id *Identity, client *http.Client, driveHost, localRoot string) *Syncer {
	return &Syncer{
		store: store, identity: id, client: client,
		driveHost: driveHost, localRoot: localRoot,
		uploaded: map[string]string{}, folders: map[string]string{},
	}
}

// Status reports what the UI needs to tell the truth about persistence.
type Status struct {
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	LastSync  time.Time `json:"last_sync,omitempty"`
	Files     int       `json:"files"`
	LastError string    `json:"last_error,omitempty"`
}

func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{LastSync: s.last, Files: s.files, LastError: s.lastErr}
	sub := currentSubjectFn()
	switch {
	case sub == "":
		st.Reason = "no signed-in user is bound to this harness yet"
	case !s.store.Granted(sub).Usable():
		st.Reason = "sessions are kept in this enclave only — approve a storage folder to keep them in your own Drive"
	case s.driveHost == "":
		st.Reason = "no Drive host is configured for this deployment"
	default:
		st.Available = true
	}
	return st
}

// currentSubjectFn is injected by the command layer, which owns the acting
// subject. Keeping it a variable avoids the internal package reaching back
// into main.
var currentSubjectFn = func() string { return "" }

// SetSubjectSource wires the acting-subject lookup.
func SetSubjectSource(fn func() string) { currentSubjectFn = fn }

// Start runs the mirror on an interval. Failures are logged and retried; a
// Drive outage must degrade to "not yet mirrored", never to a stalled harness
// or a lost session.
func (s *Syncer) Start(interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if err := s.SyncOnce(); err != nil {
				s.mu.Lock()
				s.lastErr = err.Error()
				s.mu.Unlock()
			}
		}
	}()
}

// SyncOnce mirrors every changed file once.
func (s *Syncer) SyncOnce() error {
	sub := currentSubjectFn()
	if sub == "" || s.driveHost == "" {
		return nil // nothing to do; Status explains why
	}
	g := s.store.Granted(sub)
	if !g.Usable() {
		return nil
	}
	// Restore before the first mirror. At boot no subject is bound yet, so a
	// restore attempted there always no-ops; the moment a holder signs in and
	// the local store is empty, THIS is where their sessions come back.
	if err := s.Restore(); err != nil {
		log.Printf("[sync] restore: %v", err)
	}

	ds, err := NewDriveStore(s.client, s.driveHost, s.identity, g)
	if err != nil {
		return err
	}

	var changed, failed int
	walkErr := filepath.WalkDir(s.localRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry must not abort the whole mirror: the rest of
			// the holder's sessions still deserve to reach their Drive.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(s.localRoot, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == syncStateName {
			// Our own bookkeeping. Mirroring it would upload a file that
			// changes on every pass, so the mirror would never go quiet.
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])

		s.mu.Lock()
		unchanged := s.uploaded[rel] == hash
		s.mu.Unlock()
		if unchanged {
			return nil
		}

		parent, folderErr := s.ensurePath(ds, filepath.ToSlash(filepath.Dir(rel)))
		if folderErr != nil {
			failed++
			log.Printf("[sync] folder for %s: %v", rel, folderErr)
			return nil
		}
		if _, putErr := ds.PutIn(parent, filepath.Base(rel), data); putErr != nil {
			failed++
			log.Printf("[sync] upload %s: %v", rel, putErr)
			return nil
		}
		s.mu.Lock()
		s.uploaded[rel] = hash
		s.mu.Unlock()
		changed++
		return nil
	})

	s.mu.Lock()
	s.last = time.Now().UTC()
	s.files = len(s.uploaded)
	if failed == 0 {
		s.lastErr = ""
	}
	s.mu.Unlock()

	if changed > 0 || failed > 0 {
		log.Printf("[sync] mirrored %d file(s) to the holder's Drive, %d failed", changed, failed)
	}
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return walkErr
	}
	if failed > 0 {
		return fmt.Errorf("capability: %d file(s) could not be mirrored", failed)
	}
	return nil
}

// ensurePath resolves (creating as needed) the Drive folder for one relative
// directory, keeping dsh's project/session shape rather than flattening it.
func (s *Syncer) ensurePath(ds *DriveStore, relDir string) (string, error) {
	if relDir == "." || relDir == "" || relDir == string(filepath.Separator) {
		return ds.nodeID(), nil
	}
	s.mu.Lock()
	if id, ok := s.folders[relDir]; ok {
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()

	parent := ds.nodeID()
	var walked string
	for _, seg := range strings.Split(relDir, "/") {
		if seg == "" || seg == "." {
			continue
		}
		walked = strings.TrimPrefix(walked+"/"+seg, "/")
		s.mu.Lock()
		cached, ok := s.folders[walked]
		s.mu.Unlock()
		if ok {
			parent = cached
			continue
		}
		id, err := ds.EnsureFolder(parent, seg)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		s.folders[walked] = id
		s.mu.Unlock()
		parent = id
	}
	return parent, nil
}

// Restore pulls the holder's stored sessions back down when the local store is
// empty — a redeployed enclave, or a new one. This is what makes the Drive copy
// the record rather than a backup nobody ever reads.
func (s *Syncer) Restore() error {
	sub := currentSubjectFn()
	if sub == "" || s.driveHost == "" {
		return nil
	}
	g := s.store.Granted(sub)
	if !g.Usable() {
		return nil
	}
	if entries, err := os.ReadDir(s.localRoot); err == nil && len(entries) > 0 {
		// Local content exists. Restoring over it could resurrect a session the
		// holder deleted, or overwrite a newer local log with an older remote
		// one — neither is a call this code should make silently.
		return nil
	}
	ds, err := NewDriveStore(s.client, s.driveHost, s.identity, g)
	if err != nil {
		return err
	}
	n, err := s.restoreInto(ds, ds.nodeID(), s.localRoot)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Printf("[sync] restored %d session file(s) from the holder's Drive", n)
	}
	return nil
}

func (s *Syncer) restoreInto(ds *DriveStore, nodeID, dir string) (int, error) {
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range children {
		target := filepath.Join(dir, c.Name)
		if strings.EqualFold(c.Kind, "folder") || strings.EqualFold(c.Kind, "dir") {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return count, err
			}
			n, err := s.restoreInto(ds, c.ID, target)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		data, err := ds.Get(c.ID)
		if err != nil {
			log.Printf("[sync] restore %s: %v", c.Name, err)
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return count, err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// stateFile persists the uploaded-hash map so a restart does not re-upload
// every session. Best-effort: losing it costs one redundant mirror pass.
const syncStateName = ".privasys-sync.json"

func (s *Syncer) statePath() string { return filepath.Join(s.localRoot, syncStateName) }

func (s *Syncer) LoadState() {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil {
		s.mu.Lock()
		s.uploaded = m
		s.files = len(m)
		s.mu.Unlock()
	}
}

func (s *Syncer) SaveState() {
	s.mu.Lock()
	keys := make([]string, 0, len(s.uploaded))
	for k := range s.uploaded {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m := make(map[string]string, len(keys))
	for _, k := range keys {
		m[k] = s.uploaded[k]
	}
	s.mu.Unlock()
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.statePath(), raw, 0o600)
}
