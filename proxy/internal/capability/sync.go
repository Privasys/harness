// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// Carrying the holder's data out to their Drive, and back.
//
// Two roots, two shapes, kept apart so a person opening the folder sees their
// conversations and their files as separate things:
//
//   - sessions/   dsh's session logs, one Drive file per local file. They are
//                 records the holder may open and read, so they keep dsh's
//                 own project/session layout.
//   - workspace/  the working tree, as a content-addressed SNAPSHOT in the
//                 format Drive renders as one item: `.workspace.json` (the
//                 tree manifest) beside `.blobs/<sha256>` (each distinct file
//                 content once). Never file-per-node: a working tree has
//                 thousands of small files and would otherwise become
//                 thousands of rows, names and change-feed entries on every
//                 save (plans/drive-as-remote-disk.md, tier C decision).
//
// Both roots are tmpfs. The enclave holds no durable user data, so this is
// not a backup: it is where the data lives, and the interval is the exposure
// window. Uploads are content-addressed, so a quiet harness sends nothing.

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

const (
	sessionsFolder  = "sessions"
	workspaceFolder = "workspace"
	blobsFolder     = ".blobs"
	manifestName    = ".workspace.json"
	// maxSnapshotFile bounds one blob. Drive's inline write cap is 64 MiB;
	// anything larger is a build artefact or a dataset, not a working file
	// worth carrying on every save, and is left out with a log line.
	maxSnapshotFile = 32 << 20
)

// snapshotSkipDirs are tool caches and dependency trees that any build
// recreates and that would dominate the snapshot many times over. `.git`
// is deliberately NOT here: it is the holder's history.
var snapshotSkipDirs = map[string]bool{
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	".pnpm-store": true, ".cache": true, ".mypy_cache": true, ".pytest_cache": true,
	".turbo": true, ".next": true, "target": true,
}

// Syncer mirrors a local session root and snapshots a workspace root into one
// holder's Drive folder.
type Syncer struct {
	broker    *Broker
	client    *http.Client
	driveHost string
	appID     string
	sessions  string // local session root
	workspace string // local workspace root

	mu sync.Mutex
	// uploaded maps a session-relative path to the content hash last stored,
	// so an unchanged file is never re-sent.
	uploaded map[string]string
	// blobs is the set of blob hashes known to exist in Drive's .blobs/.
	blobs map[string]bool
	// folders caches Drive path -> node id for the session tree.
	folders map[string]string
	// treeSeen/treeSaved are the workspace tree hashes last observed and last
	// snapshotted: a snapshot is taken only once the tree has held still for
	// a whole tick, so a file mid-write is never captured torn.
	treeSeen  string
	treeSaved string
	savedAt   time.Time
	last      time.Time
	lastErr   string
	files     int
	blobCount int
	// restoredFor is the subject whose data was already restored this
	// process, so a restore is attempted once per sign-in rather than per tick.
	restoredFor string
}

// NewSyncer wires the two local roots to the holder's Drive folder. appID is
// this app's platform id, recorded in every snapshot manifest.
func NewSyncer(broker *Broker, client *http.Client, driveHost, appID, sessionsRoot, workspaceRoot string) *Syncer {
	return &Syncer{
		broker: broker, client: client, driveHost: driveHost, appID: appID,
		sessions: sessionsRoot, workspace: workspaceRoot,
		uploaded: map[string]string{}, blobs: map[string]bool{}, folders: map[string]string{},
	}
}

// Status reports what the UI needs to tell the truth about persistence.
type SyncStatus struct {
	Available        bool      `json:"available"`
	Reason           string    `json:"reason,omitempty"`
	Folder           string    `json:"folder,omitempty"`
	LastSync         time.Time `json:"last_sync,omitempty"`
	Files            int       `json:"files"`
	Blobs            int       `json:"blobs"`
	WorkspaceSavedAt time.Time `json:"workspace_saved_at,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
}

func (s *Syncer) Status() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := SyncStatus{LastSync: s.last, Files: s.files, Blobs: s.blobCount, WorkspaceSavedAt: s.savedAt, LastError: s.lastErr}
	sub := currentSubjectFn()
	switch {
	case sub == "":
		st.Reason = "no signed-in user is bound to this harness yet"
	case !s.broker.Enabled():
		st.Reason = "no runtime broker: this harness is not running on the platform"
	case !s.broker.Granted(sub).Usable():
		st.Reason = "sessions are kept in memory only — connect your Drive to keep them"
	case s.driveHost == "":
		st.Reason = "no Drive host is configured for this deployment"
	default:
		st.Available = true
		st.Folder = s.broker.Granted(sub).Path()
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

// store resolves the acting holder's capability into a working store, or
// nil when there is nothing to do yet.
func (s *Syncer) store() (*DriveStore, string) {
	sub := currentSubjectFn()
	if sub == "" || s.driveHost == "" || !s.broker.Enabled() {
		return nil, sub
	}
	g := s.broker.Granted(sub)
	if !g.Usable() {
		return nil, sub
	}
	ds, err := NewDriveStore(s.client, s.driveHost, s.broker, g)
	if err != nil {
		return nil, sub
	}
	return ds, sub
}

// SyncOnce mirrors every changed session file and, when the working tree has
// held still for a tick, snapshots it.
func (s *Syncer) SyncOnce() error {
	ds, sub := s.store()
	if ds == nil {
		return nil // nothing to do; Status explains why
	}
	// Restore before the first mirror. At boot no subject is bound yet, so a
	// restore attempted there always no-ops; the moment a holder signs in and
	// the local roots are empty, THIS is where their data comes back.
	s.mu.Lock()
	needRestore := s.restoredFor != sub
	s.mu.Unlock()
	if needRestore {
		if err := s.restore(ds); err != nil {
			log.Printf("[sync] restore: %v", err)
		} else {
			s.mu.Lock()
			s.restoredFor = sub
			s.mu.Unlock()
		}
	}

	sessErr := s.mirrorSessions(ds)
	wsErr := s.snapshotWorkspace(ds)

	s.mu.Lock()
	s.last = time.Now().UTC()
	s.files = len(s.uploaded)
	s.blobCount = len(s.blobs)
	if sessErr == nil && wsErr == nil {
		s.lastErr = ""
	}
	s.mu.Unlock()
	if sessErr != nil {
		return sessErr
	}
	return wsErr
}

// ---- sessions: one Drive file per local file --------------------------------

func (s *Syncer) mirrorSessions(ds *DriveStore) error {
	var changed, failed int
	walkErr := filepath.WalkDir(s.sessions, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry must not abort the whole mirror: the rest of
			// the holder's sessions still deserve to reach their Drive.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(s.sessions, path)
		if relErr != nil {
			return nil
		}
		rel = sessionsFolder + "/" + filepath.ToSlash(rel)
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
	if walkErr != nil && !os.IsNotExist(walkErr) {
		log.Printf("[sync] walking %s: %v", s.sessions, walkErr)
	}
	if changed > 0 || failed > 0 {
		log.Printf("[sync] mirrored %d session file(s) to the holder's Drive, %d failed", changed, failed)
	}
	if failed > 0 {
		return fmt.Errorf("capability: %d session file(s) could not be mirrored", failed)
	}
	return nil
}

// ensurePath resolves (creating as needed) the Drive folder for one relative
// directory, keeping dsh's project/session shape rather than flattening it.
func (s *Syncer) ensurePath(ds *DriveStore, relDir string) (string, error) {
	if relDir == "." || relDir == "" || relDir == "/" {
		return ds.RootID(), nil
	}
	s.mu.Lock()
	if id, ok := s.folders[relDir]; ok {
		s.mu.Unlock()
		return id, nil
	}
	s.mu.Unlock()

	parent := ds.RootID()
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

// ---- workspace: a content-addressed snapshot ---------------------------------

// manifestFile is one entry of `.workspace.json` (Drive's workspace
// snapshot contract, plans/drive-as-remote-disk.md 2026-09-08).
type manifestFile struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Mode string `json:"mode"`
	Blob string `json:"blob"`
}

type manifest struct {
	Version int            `json:"version"`
	App     string         `json:"app"`
	SavedAt string         `json:"saved_at"`
	Files   []manifestFile `json:"files"`
}

// scanWorkspace walks the local tree and returns its manifest entries (blob
// hashes computed) plus a hash of the whole tree, cheap to compare tick to
// tick.
func (s *Syncer) scanWorkspace() ([]manifestFile, map[string][]byte, string) {
	var files []manifestFile
	contents := map[string][]byte{}
	walkErr := filepath.WalkDir(s.workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != s.workspace && snapshotSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // sockets, devices, symlinks: not part of a portable tree
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > maxSnapshotFile {
			return nil
		}
		rel, relErr := filepath.Rel(s.workspace, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == manifestName || strings.HasPrefix(rel, blobsFolder+"/") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		files = append(files, manifestFile{
			Path: rel, Size: int64(len(data)), Mode: fmt.Sprintf("%04o", info.Mode().Perm()), Blob: hash,
		})
		contents[hash] = data
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		log.Printf("[sync] walking %s: %v", s.workspace, walkErr)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", f.Path, f.Mode, f.Blob)
	}
	return files, contents, hex.EncodeToString(h.Sum(nil))
}

// snapshotWorkspace uploads the blobs Drive does not yet hold and then the
// manifest, once the tree has held still for one tick.
func (s *Syncer) snapshotWorkspace(ds *DriveStore) error {
	files, contents, tree := s.scanWorkspace()
	if len(files) == 0 {
		return nil // an empty workspace is not a snapshot worth taking
	}
	s.mu.Lock()
	settled := s.treeSeen == tree
	alreadySaved := s.treeSaved == tree
	s.treeSeen = tree
	s.mu.Unlock()
	if alreadySaved || !settled {
		return nil
	}

	wsID, err := s.ensurePath(ds, workspaceFolder)
	if err != nil {
		return err
	}
	blobsID, err := s.ensurePath(ds, workspaceFolder+"/"+blobsFolder)
	if err != nil {
		return err
	}
	// Learn what Drive already holds once, so a fresh process (its state is
	// tmpfs and dies with the container) does not re-send every blob.
	s.mu.Lock()
	known := len(s.blobs) > 0
	s.mu.Unlock()
	if !known {
		if existing, lerr := ds.ListIn(blobsID); lerr == nil {
			s.mu.Lock()
			for _, n := range existing {
				if !n.IsFolder() {
					s.blobs[n.Name] = true
				}
			}
			s.mu.Unlock()
		}
	}

	var sent, failed int
	for hash, data := range contents {
		s.mu.Lock()
		have := s.blobs[hash]
		s.mu.Unlock()
		if have {
			continue
		}
		if _, perr := ds.PutIn(blobsID, hash, data); perr != nil {
			failed++
			log.Printf("[sync] blob %s: %v", hash[:12], perr)
			continue
		}
		s.mu.Lock()
		s.blobs[hash] = true
		s.mu.Unlock()
		sent++
	}
	if failed > 0 {
		return fmt.Errorf("capability: %d workspace blob(s) could not be stored", failed)
	}

	now := time.Now().UTC()
	body, err := json.MarshalIndent(manifest{Version: 1, App: s.appID, SavedAt: now.Format(time.RFC3339), Files: files}, "", " ")
	if err != nil {
		return err
	}
	if _, err := ds.PutIn(wsID, manifestName, body); err != nil {
		return fmt.Errorf("capability: workspace manifest: %w", err)
	}
	s.mu.Lock()
	s.treeSaved = tree
	s.savedAt = now
	s.mu.Unlock()
	log.Printf("[sync] workspace snapshot saved: %d file(s), %d new blob(s)", len(files), sent)
	return nil
}

// ---- restore ------------------------------------------------------------------

// restore pulls the holder's stored data back down when the local roots are
// empty — a redeployed enclave, or a new one. This is what makes the Drive
// copy the record rather than a backup nobody ever reads.
func (s *Syncer) restore(ds *DriveStore) error {
	if !dirEmpty(s.sessions) && !dirEmpty(s.workspace) {
		// Local content exists. Restoring over it could resurrect a session
		// the holder deleted, or overwrite a newer local file with an older
		// remote one — neither is a call this code should make silently.
		return nil
	}
	var firstErr error
	if dirEmpty(s.sessions) {
		if id, err := s.folderIfExists(ds, sessionsFolder); err != nil {
			firstErr = err
		} else if id != "" {
			n, rerr := s.restoreTree(ds, id, s.sessions)
			if rerr != nil && firstErr == nil {
				firstErr = rerr
			}
			if n > 0 {
				log.Printf("[sync] restored %d session file(s) from the holder's Drive", n)
			}
		}
	}
	if dirEmpty(s.workspace) {
		if id, err := s.folderIfExists(ds, workspaceFolder); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else if id != "" {
			n, rerr := s.restoreWorkspace(ds, id)
			if rerr != nil && firstErr == nil {
				firstErr = rerr
			}
			if n > 0 {
				log.Printf("[sync] restored %d workspace file(s) from the holder's Drive", n)
			}
		}
	}
	return firstErr
}

func dirEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err != nil || len(entries) == 0
}

// folderIfExists returns the id of a top-level folder in the granted node, or
// "" when it does not exist. It never creates: restore must not leave marks.
func (s *Syncer) folderIfExists(ds *DriveStore, name string) (string, error) {
	children, err := ds.List()
	if err != nil {
		return "", err
	}
	for _, c := range children {
		if c.Name == name && c.IsFolder() {
			return c.ID, nil
		}
	}
	return "", nil
}

// restoreTree materialises a file-per-node Drive folder locally (sessions,
// and workspaces saved before the snapshot format).
func (s *Syncer) restoreTree(ds *DriveStore, nodeID, dir string) (int, error) {
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range children {
		if c.Name == manifestName || c.Name == blobsFolder {
			continue
		}
		target := filepath.Join(dir, c.Name)
		if c.IsFolder() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return count, err
			}
			n, err := s.restoreTree(ds, c.ID, target)
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

// restoreWorkspace rebuilds the working tree from the snapshot manifest,
// falling back to the file-per-node layout of snapshots taken before the
// manifest format existed.
func (s *Syncer) restoreWorkspace(ds *DriveStore, wsID string) (int, error) {
	children, err := ds.ListIn(wsID)
	if err != nil {
		return 0, err
	}
	var manifestID, blobsID string
	for _, c := range children {
		switch {
		case c.Name == manifestName && !c.IsFolder():
			manifestID = c.ID
		case c.Name == blobsFolder && c.IsFolder():
			blobsID = c.ID
		}
	}
	if manifestID == "" || blobsID == "" {
		return s.restoreTree(ds, wsID, s.workspace)
	}
	raw, err := ds.Get(manifestID)
	if err != nil {
		return 0, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, fmt.Errorf("capability: workspace manifest unreadable: %w", err)
	}
	blobNodes, err := ds.ListIn(blobsID)
	if err != nil {
		return 0, err
	}
	byName := map[string]string{}
	for _, b := range blobNodes {
		byName[b.Name] = b.ID
	}
	fetched := map[string][]byte{}
	count := 0
	for _, f := range m.Files {
		rel := filepath.Clean(filepath.FromSlash(f.Path))
		if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			continue // an escaping path is dropped, as Drive's own export does
		}
		data, ok := fetched[f.Blob]
		if !ok {
			id := byName[f.Blob]
			if id == "" {
				log.Printf("[sync] restore %s: blob %s missing in Drive", f.Path, f.Blob[:12])
				continue
			}
			data, err = ds.Get(id)
			if err != nil {
				log.Printf("[sync] restore %s: %v", f.Path, err)
				continue
			}
			fetched[f.Blob] = data
		}
		target := filepath.Join(s.workspace, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return count, err
		}
		mode := os.FileMode(0o600)
		if parsed, perr := parseMode(f.Mode); perr == nil {
			mode = parsed
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return count, err
		}
		count++
	}
	// What came down is exactly what Drive holds: seed the caches so the next
	// tick does not re-send it all.
	s.mu.Lock()
	for name := range byName {
		s.blobs[name] = true
	}
	s.mu.Unlock()
	return count, nil
}

func parseMode(s string) (os.FileMode, error) {
	var m uint32
	if _, err := fmt.Sscanf(s, "%o", &m); err != nil {
		return 0, err
	}
	return os.FileMode(m) & os.ModePerm, nil
}

// ---- state -------------------------------------------------------------------

// The uploaded-hash and blob maps persist across a proxy restart within one
// container so a restart does not re-send every session. Best-effort, and on
// tmpfs: they are DERIVED from the holder's files, so they are theirs too and
// must not outlive the container either.
const syncStateName = ".privasys-sync.json"

func (s *Syncer) statePath() string { return filepath.Join("/run", syncStateName) }

type syncState struct {
	Uploaded map[string]string `json:"uploaded"`
	Blobs    []string          `json:"blobs"`
	Tree     string            `json:"tree"`
	SavedAt  time.Time         `json:"saved_at"`
}

func (s *Syncer) LoadState() {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		return
	}
	var st syncState
	if json.Unmarshal(raw, &st) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.Uploaded != nil {
		s.uploaded = st.Uploaded
		s.files = len(st.Uploaded)
	}
	for _, b := range st.Blobs {
		s.blobs[b] = true
	}
	s.blobCount = len(s.blobs)
	s.treeSaved, s.treeSeen, s.savedAt = st.Tree, st.Tree, st.SavedAt
}

func (s *Syncer) SaveState() {
	s.mu.Lock()
	st := syncState{Uploaded: map[string]string{}, Tree: s.treeSaved, SavedAt: s.savedAt}
	for k, v := range s.uploaded {
		st.Uploaded[k] = v
	}
	for b := range s.blobs {
		st.Blobs = append(st.Blobs, b)
	}
	s.mu.Unlock()
	sort.Strings(st.Blobs)
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.statePath(), raw, 0o600)
}
