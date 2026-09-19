// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// Carrying the holder's conversations out to their Drive, and back.
//
// Drive holds the memory; the holder folder holds the agent (plan §5.9,
// 2026-09-19). This mirror does one thing: dsh's CHAT session logs go to
// `sessions/` in the holder's Drive, one file per local file, filed under
// the workspace they belong to (and Archived/ once archived); sessions.go
// owns that mapping and the way back. A session run by one of the holder's
// agents stays in the holder folder with the agent. Everything else the
// agent works with (definitions, skills, outputs, working trees, the dsh
// home) lives in the holder folder and never passes through here.
//
// Uploads are content-addressed, so a quiet harness sends nothing.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	sessionsFolder = "sessions"
)

// isLockFile recognises dsh's per-session `session.lock` (a kernel flock
// target with no content of its own) and any other lock marker. Mirroring
// one is pointless and restoring one is misleading: the lock's meaning is
// the process holding it, which never crosses machines.
func isLockFile(name string) bool {
	return name == "session.lock" || strings.HasSuffix(name, ".lock")
}

// Syncer mirrors a local session root into one holder's Drive folder, and
// knows the holder's agents (agents.go) so it can tell their runs apart.
type Syncer struct {
	broker    *Broker
	client    *http.Client
	driveHost string
	appID     string
	sessions  string // local session root
	workspace string // local workspace root (only the agent workspaces are read here)
	// agentsRoot is where the holder's agents live (the workspace root, one
	// directory per agent, agents.go); empty means no agents.
	agentsRoot string
	agentUID   int
	// subject binds this syncer to one person (per-user workers); empty
	// means the process-wide acting subject (single-user layout).
	subject string
	// stateFile overrides the default state path (per worker).
	stateFile string
	// stop ends the tick loop started by Start.
	stop chan struct{}
	// OnWithdrawn, when set, is called once (off the tick loop) when Drive
	// first refuses the holder's capability. The loop ends with it; a fresh
	// approval is a runtime event that starts it again, so nothing keeps
	// asking a Drive that said no.
	OnWithdrawn func()

	mu sync.Mutex
	// uploaded maps a session-relative path to the content hash last stored,
	// so an unchanged file is never re-sent.
	uploaded map[string]string
	// folders caches Drive path -> node id for the session tree.
	folders map[string]string
	// grantSeen is the grant the caches above were built under. A fresh
	// approval is a fresh folder: every cached node id and uploaded hash
	// then names something that no longer exists.
	grantSeen string
	last      time.Time
	lastErr   string
	files     int
	// restoredFor is the subject whose data was already restored this
	// process, so a restore is attempted once per sign-in rather than per tick.
	restoredFor string
	// ready flips once the boot-time restore has run (or been found
	// inapplicable), so the entrypoint can hold dsh back until the holder's
	// data is on disk: dsh builds its workspace list ONCE at start and never
	// re-bootstraps, so a session restored after that start is invisible.
	ready bool
	// withdrawn is set when Drive refuses the capability itself (401/403)
	// and cleared by the next pass Drive accepts. The runtime cannot see a
	// revoke made in Drive, so this is the only place the truth surfaces.
	withdrawn bool
	// withdrawnGrant is the grant id the refused calls presented, so a fresh
	// approval (a different grant) clears the flag before the next pass.
	withdrawnGrant string
	// passRefused is set by the first refusal of the current pass and reset
	// when the next pass starts; a later step's success cannot clear it.
	passRefused bool
	// registryFile is dsh's workspace registry document for this holder
	// (titles, paths, archived ids); sessions.go reads it every pass.
	registryFile string
	// metaCache remembers each local session directory's header (id, cwd):
	// neither ever changes, and the file is read once instead of per tick.
	metaCache map[string]sessionMeta
	// deleteRefused/pendingDeletes report a Drive that has not granted
	// `delete` yet: stale copies wait there until the holder approves it.
	deleteRefused  bool
	pendingDeletes int
}

// AccessWithdrawn reports whether Drive last refused the holder's capability.
// A refusal is remembered WITH the grant it was answered to: the moment the
// runtime records a different grant (the holder approved afresh on their
// device), the refusal no longer describes anything, and the row must not
// keep saying "Withdrawn" until the next mirror pass happens to prove the
// new grant (2026-09-16: that wait read as a frozen UI after the tap).
func (s *Syncer) AccessWithdrawn() bool {
	s.mu.Lock()
	withdrawn, refusedGrant := s.withdrawn, s.withdrawnGrant
	s.mu.Unlock()
	if !withdrawn {
		return false
	}
	if sub := s.subjectNow(); sub != "" && s.broker != nil && refusedGrant != "" {
		if g := s.broker.Granted(sub); g.Usable() && g.GrantID() != refusedGrant {
			return false
		}
	}
	return true
}

// noteOutcome records whether Drive accepted the capability on this pass.
// refusedGrant names the grant the refused calls presented.
func (s *Syncer) noteOutcome(err error, refusedGrant string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		// A step with nothing to send succeeds without touching Drive, so
		// it must not clear a refusal another step of the SAME pass just
		// noted (2026-09-16: the flag flapped every pass and the row fell
		// back to "In your Drive"). Only a pass free of refusals clears it.
		if s.passRefused {
			return
		}
		s.withdrawn = false
		s.withdrawnGrant = ""
	case IsRefused(err):
		if !s.withdrawn {
			log.Printf("[sync] Drive refused the holder's capability; access looks withdrawn: %v", err)
		}
		s.withdrawn = true
		s.withdrawnGrant = refusedGrant
		s.passRefused = true
	}
}

// Ready reports whether the boot-time restore has completed.
func (s *Syncer) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

// SetReady marks the boot-time restore as done, whatever its outcome.
func (s *Syncer) SetReady() {
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
}

// RestoreFor pulls one holder's data down before dsh starts, for the subject
// this deployment remembered from its last sign-in. Returns how many files
// came back. Best-effort: a Drive outage or a revoked grant leaves the roots
// empty, exactly as a fresh harness would be, and the mirror's normal restore
// retries when the holder signs in.
func (s *Syncer) RestoreFor(subject string) (int, error) {
	if subject == "" || s.driveHost == "" || !s.broker.Enabled() {
		return 0, nil
	}
	g := s.broker.Granted(subject)
	if !g.Usable() {
		return 0, nil
	}
	ds, err := NewDriveStore(s.client, s.driveHost, s.broker, g)
	if err != nil {
		return 0, err
	}
	n, err := s.restore(ds)
	if err == nil {
		s.mu.Lock()
		s.restoredFor = subject
		s.mu.Unlock()
	} else if IsRefused(err) {
		// Known before the first tick: the worker then runs without a
		// mirror rather than starting one that would stop it 15 s later.
		s.noteOutcome(err, g.GrantID())
	}
	return n, err
}

// Ticking reports whether the mirror loop started by Start is running.
func (s *Syncer) Ticking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stop != nil
}

// NewSyncer wires the local session root to the holder's Drive folder; the
// workspace root is where the holder's agents live (agents.go). The acting
// subject is the process-wide one (single-user layout).
func NewSyncer(broker *Broker, client *http.Client, driveHost, appID, sessionsRoot, workspaceRoot string) *Syncer {
	return &Syncer{
		broker: broker, client: client, driveHost: driveHost, appID: appID,
		sessions: sessionsRoot, workspace: workspaceRoot,
		uploaded: map[string]string{}, folders: map[string]string{},
		metaCache: map[string]sessionMeta{},
	}
}

// NewSyncerFor is NewSyncer bound to one subject: the per-user worker layout,
// where each worker's roots belong to exactly one person.
func NewSyncerFor(broker *Broker, client *http.Client, driveHost, appID, sessionsRoot, workspaceRoot, subject string) *Syncer {
	s := NewSyncer(broker, client, driveHost, appID, sessionsRoot, workspaceRoot)
	s.subject = subject
	s.stateFile = filepath.Join(filepath.Dir(sessionsRoot), syncStateName)
	return s
}

// subjectNow is the subject this syncer works for: its own when bound, else
// the process-wide acting subject.
func (s *Syncer) subjectNow() string {
	if s.subject != "" {
		return s.subject
	}
	return currentSubjectFn()
}

// Status reports what the UI needs to tell the truth about persistence.
type SyncStatus struct {
	Available       bool      `json:"available"`
	AccessWithdrawn bool      `json:"access_withdrawn,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Folder          string    `json:"folder,omitempty"`
	LastSync        time.Time `json:"last_sync,omitempty"`
	Files           int       `json:"files"`
	LastError       string    `json:"last_error,omitempty"`
	// DeleteRefused: Drive has not granted this harness `delete`, so
	// PendingDeletes stale session copies remain on the holder's Drive.
	DeleteRefused  bool `json:"delete_refused,omitempty"`
	PendingDeletes int  `json:"pending_deletes,omitempty"`
}

func (s *Syncer) Status() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := SyncStatus{LastSync: s.last, Files: s.files, LastError: s.lastErr, AccessWithdrawn: s.withdrawn,
		DeleteRefused: s.deleteRefused, PendingDeletes: s.pendingDeletes}
	sub := s.subjectNow()
	switch {
	case sub == "":
		st.Reason = "no signed-in user is bound to this harness yet"
	case !s.broker.Enabled():
		st.Reason = "no runtime broker: this harness is not running on the platform"
	case !s.broker.Granted(sub).Usable():
		st.Reason = "conversations are not kept anywhere yet; connect your Drive to keep them"
	case s.driveHost == "":
		st.Reason = "no Drive host is configured for this deployment"
	case s.withdrawn:
		st.Reason = "your Drive refused this harness's access — it was withdrawn or has expired; connect your Drive again"
		st.Folder = s.broker.Granted(sub).Path()
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
	stop := make(chan struct{})
	s.mu.Lock()
	s.stop = stop
	s.mu.Unlock()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			if err := s.SyncOnce(); err != nil {
				s.mu.Lock()
				s.lastErr = err.Error()
				s.mu.Unlock()
			}
			// A Drive that refused the grant is not asked again on a timer:
			// the loop ends here, and the owner is told once.
			if s.AccessWithdrawn() {
				s.StopTicks()
				if s.OnWithdrawn != nil {
					s.OnWithdrawn()
				}
				return
			}
		}
	}()
}

// StopTicks ends the loop started by Start. The syncer stays usable for a
// final SyncOnce.
func (s *Syncer) StopTicks() {
	s.mu.Lock()
	stop := s.stop
	s.stop = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// store resolves the acting holder's capability into a working store, or
// nil when there is nothing to do yet.
func (s *Syncer) store() (*DriveStore, string) {
	sub := s.subjectNow()
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
		if _, err := s.restore(ds); err != nil {
			log.Printf("[sync] restore: %v", err)
		} else {
			s.mu.Lock()
			s.restoredFor = sub
			s.mu.Unlock()
		}
	}

	// The holder's own behaviour files, read from their Drive every pass: this
	// is what makes the assistant a folder they edit rather than a build.
	// Failures here never hold up the mirror; the agent simply runs on what it
	// has, which is the deployment's reference skills at worst.
	grant := ds.grantID()
	s.mu.Lock()
	s.passRefused = false
	if s.grantSeen != "" && s.grantSeen != grant {
		// The holder approved afresh (after withdrawing, or a new folder):
		// the node ids and the uploaded hashes belong to the old folder.
		// Kept, they turned every write into a 403 and left "Withdrawn" on
		// the row for as long as the caches lived (2026-09-16 20:49).
		log.Printf("[sync] a new grant for %.8s…: forgetting the folder ids and upload marks of the old one", s.subject)
		s.folders = map[string]string{}
		s.uploaded = map[string]string{}
	}
	s.grantSeen = grant
	s.mu.Unlock()
	sessErr := s.mirrorSessions(ds)
	s.noteOutcome(sessErr, grant)

	s.mu.Lock()
	s.last = time.Now().UTC()
	s.files = len(s.uploaded)
	if sessErr == nil {
		s.lastErr = ""
	}
	s.mu.Unlock()
	return sessErr
}

// contentHash is the hex SHA-256 the uploaded-file map is keyed by.
func contentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
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

// ---- restore ------------------------------------------------------------------

// restore pulls the holder's stored data back down when the local roots are
// empty — a redeployed enclave, or a new one. This is what makes the Drive
// copy the record rather than a backup nobody ever reads. Returns the number
// of files materialised.
func (s *Syncer) restore(ds *DriveStore) (int, error) {
	if !dirEmpty(s.sessions) {
		// Local conversations exist (the holder folder keeps them across
		// restarts). Restoring over them could resurrect a session the
		// holder deleted, or overwrite a newer local file with an older
		// remote one; neither is a call this code should make silently.
		return 0, nil
	}
	id, err := s.folderIfExists(ds, sessionsFolder)
	if err != nil || id == "" {
		return 0, err
	}
	n, rerr := s.restoreSessions(ds, id)
	if n > 0 {
		log.Printf("[sync] restored %d session file(s) from the holder's Drive", n)
		// A session whose working directory is missing is invisible to dsh:
		// recreate it from the header.
		if made := s.ensureSessionCwds(); made > 0 {
			log.Printf("[sync] recreated %d session working director%s", made, map[bool]string{true: "y", false: "ies"}[made == 1])
		}
	}
	return n, rerr
}

// ensureSessionCwds recreates, under the workspace root, the working
// directory each restored session log names in its header. dsh's session
// directory name is only a readable rendering of the cwd, so the header is
// read: the first line of the log, in the first Zstandard frame of a
// compressed log or plain in a raw one. Only paths inside the workspace root
// are created; a session that ran elsewhere is left as it is.
func (s *Syncer) ensureSessionCwds() int {
	root := filepath.Clean(s.workspace)
	made := 0
	seen := map[string]bool{}
	_ = filepath.WalkDir(s.sessions, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "session") || !strings.Contains(name, ".jsonl") {
			return nil
		}
		cwd := sessionCwd(path)
		if cwd == "" {
			return nil
		}
		cwd = filepath.Clean(cwd)
		if cwd != root && !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
			return nil
		}
		if seen[cwd] {
			return nil
		}
		seen[cwd] = true
		if _, statErr := os.Stat(cwd); statErr == nil {
			return nil
		}
		if os.MkdirAll(cwd, 0o700) == nil {
			made++
		}
		return nil
	})
	return made
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

// restoreTree materialises a file-per-node Drive folder locally.
func (s *Syncer) restoreTree(ds *DriveStore, nodeID, dir string) (int, error) {
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range children {
		if isLockFile(c.Name) || strings.HasPrefix(c.Name, ".") {
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

// ---- state -------------------------------------------------------------------

// The uploaded-hash and blob maps persist across a proxy restart within one
// container so a restart does not re-send every session. Best-effort, and on
// tmpfs: they are DERIVED from the holder's files, so they are theirs too and
// must not outlive the container either.
const syncStateName = ".privasys-sync.json"

func (s *Syncer) statePath() string {
	if s.stateFile != "" {
		return s.stateFile
	}
	return filepath.Join("/run", syncStateName)
}

type syncState struct {
	Uploaded map[string]string `json:"uploaded"`
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
}

func (s *Syncer) SaveState() {
	s.mu.Lock()
	st := syncState{Uploaded: map[string]string{}}
	for k, v := range s.uploaded {
		st.Uploaded[k] = v
	}
	s.mu.Unlock()
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.statePath(), raw, 0o600)
}

// Flush mirrors what is left and reports whether the holder's Drive now
// holds every conversation this worker wrote: only then may a scratch be
// wiped (workers.go). A holder folder needs no such proof.
func (s *Syncer) Flush() bool {
	if ds, _ := s.store(); ds == nil {
		return false
	}
	for i := 0; i < 2; i++ {
		if err := s.SyncOnce(); err != nil {
			return false
		}
	}
	if s.AccessWithdrawn() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.passRefused && s.lastErr == ""
}
