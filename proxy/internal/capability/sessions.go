// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

// The sessions leg of the mirror: how dsh's session logs are laid out on the
// holder's Drive, and how they come back.
//
// Locally dsh keeps `<root>/<project key>/<session id>/session.v3.jsonl…`,
// where the project key is its own rendering of the session's working
// directory (`--data-users-…-workspace-Chat--`). That name is for a
// filesystem, not for a person opening their Drive. On Drive the same log
// lives under the WORKSPACE it belongs to:
//
//	sessions/<Workspace title>/<session id>/…
//	sessions/Archived/<Workspace title>/<session id>/…   (dsh's archive set)
//
// The mapping needs two things dsh already writes: the session log's header
// (its id and cwd) and the workspace registry (titles, paths, archived ids)
// in dsh's storage domain. Restore reads the header to put a log back in the
// directory dsh expects, so the Drive name is never load-bearing: a renamed
// workspace, an archived session or a legacy `--slug--` folder all restore
// to the right place. What no longer exists locally is deleted on Drive
// (needs the grant's `delete` permission; until it is granted the stale
// copies stay and the status says so).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/klauspost/compress/zstd"
)

const (
	archivedFolder  = "Archived"
	noWorkspaceName = "No workspace"
	noCwdProjectDir = "_no-cwd"
	maxNameLen      = 120
)

// projectKey ports dsh's session-persistence-jsonl `projectKey` (format.ts):
// separators become one `-`, unsafe UTF-16 units become `~XXXX`, the result
// is bounded and wrapped in `--…--`. Kept byte-exact so a restored log lands
// where dsh will look for it.
func projectKey(cwd string) string {
	if cwd == "" {
		return noCwdProjectDir
	}
	var b strings.Builder
	sepRun := false
	for _, u := range utf16.Encode([]rune(cwd)) {
		switch {
		case u == '/' || u == '\\' || u == ':':
			if !sepRun {
				b.WriteByte('-')
			}
			sepRun = true
		case u != '~' && (u >= 'A' && u <= 'Z' || u >= 'a' && u <= 'z' || u >= '0' && u <= '9' || u == '.' || u == '_' || u == '-'):
			b.WriteByte(byte(u))
			sepRun = false
		default:
			fmt.Fprintf(&b, "~%04X", u)
			sepRun = false
		}
	}
	slug := strings.TrimLeft(b.String(), "-")
	if slug == "" {
		slug = "root"
	}
	if len(slug) > 251 {
		slug = slug[:251]
	}
	return "--" + slug + "--"
}

// isLegacySlug recognises a Drive folder named by dsh's project key rather
// than by a workspace: the layout this mirror used before 2026-09-11.
func isLegacySlug(name string) bool {
	return name == noCwdProjectDir || (len(name) > 4 && strings.HasPrefix(name, "--") && strings.HasSuffix(name, "--"))
}

// isSessionLog recognises dsh's generation log files (session.v3.jsonl,
// session.v3.jsonl.zstd, …), the one file every session directory holds.
func isSessionLog(name string) bool {
	return strings.HasPrefix(name, "session") && strings.Contains(name, ".jsonl") && !isLockFile(name)
}

// sessionMeta is what the mirror needs from a log's header line.
type sessionMeta struct {
	ID  string
	Cwd string
}

// readSessionMeta parses the header line of one session log: the first line
// of the log, in the first Zstandard frame of a compressed one.
func readSessionMeta(logPath string) (sessionMeta, bool) {
	f, err := os.Open(logPath)
	if err != nil {
		return sessionMeta{}, false
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(logPath, ".zstd") {
		dec, derr := zstd.NewReader(f)
		if derr != nil {
			return sessionMeta{}, false
		}
		defer dec.Close()
		r = dec
	}
	line, err := bufio.NewReaderSize(r, 64<<10).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return sessionMeta{}, false
	}
	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Cwd  string `json:"cwd"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session" || header.ID == "" {
		return sessionMeta{}, false
	}
	// dsh writes POSIX paths; IsAbs alone would reject them on a Windows test host.
	if header.Cwd != "" && !filepath.IsAbs(header.Cwd) && !strings.HasPrefix(header.Cwd, "/") {
		header.Cwd = ""
	}
	return sessionMeta{ID: header.ID, Cwd: filepath.Clean(header.Cwd)}, true
}

// sessionCwd returns the `cwd` of a session log's header line, or "".
func sessionCwd(logPath string) string {
	m, ok := readSessionMeta(logPath)
	if !ok || m.Cwd == "." {
		return ""
	}
	return m.Cwd
}

// sessionDirMeta reads (and caches) the header of the log inside one session
// directory. A directory holding no readable log yields ok=false.
func (s *Syncer) sessionDirMeta(dir string) (sessionMeta, bool) {
	s.mu.Lock()
	m, hit := s.metaCache[dir]
	s.mu.Unlock()
	if hit {
		return m, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return sessionMeta{}, false
	}
	for _, e := range entries {
		if e.IsDir() || !isSessionLog(e.Name()) {
			continue
		}
		if m, ok := readSessionMeta(filepath.Join(dir, e.Name())); ok {
			s.mu.Lock()
			if s.metaCache == nil {
				s.metaCache = map[string]sessionMeta{}
			}
			s.metaCache[dir] = m
			s.mu.Unlock()
			return m, true
		}
	}
	return sessionMeta{}, false
}

// ---- the workspace registry --------------------------------------------------

// workspaceEntry is one workspace as dsh's registry records it, with the
// Drive folder name chosen for it.
type workspaceEntry struct {
	Path    string
	Title   string
	Display string
}

// registry is the slice of dsh's workspace domain the mirror reads: which
// sessions are archived, and what each workspace path is called.
type registry struct {
	archived   map[string]bool
	workspaces []workspaceEntry // longest path first
}

// loadRegistry reads dsh's `storages/workspace.json` (storage-json single
// layout: {unit, global, tables}). Missing or unreadable reads as empty: the
// mirror then names folders after the working directory and archives
// nothing, which is safe in both directions.
func loadRegistry(file string) registry {
	reg := registry{archived: map[string]bool{}}
	if file == "" {
		return reg
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return reg
	}
	var doc struct {
		Global struct {
			ArchivedSessionIds []string `json:"archivedSessionIds"`
		} `json:"global"`
		Tables struct {
			Workspaces map[string]struct {
				Path  string `json:"path"`
				Title string `json:"title"`
			} `json:"workspaces"`
		} `json:"tables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return reg
	}
	for _, id := range doc.Global.ArchivedSessionIds {
		reg.archived[id] = true
	}
	titleCount := map[string]int{}
	for _, w := range doc.Tables.Workspaces {
		titleCount[strings.TrimSpace(w.Title)]++
	}
	for id, w := range doc.Tables.Workspaces {
		if w.Path == "" {
			continue
		}
		title := strings.TrimSpace(w.Title)
		if title == "" {
			title = filepath.Base(w.Path)
		}
		display := title
		if titleCount[title] > 1 {
			// Two workspaces with one title: tell them apart by their
			// directory, then by id, so their sessions never interleave.
			display = title + " (" + filepath.Base(w.Path) + ")"
			for _, o := range doc.Tables.Workspaces {
				if o.Path != w.Path && strings.TrimSpace(o.Title) == title && filepath.Base(o.Path) == filepath.Base(w.Path) {
					display = title + " (" + shortID(id) + ")"
					break
				}
			}
		}
		reg.workspaces = append(reg.workspaces, workspaceEntry{
			Path: filepath.Clean(w.Path), Title: title, Display: sanitiseName(display),
		})
	}
	sort.Slice(reg.workspaces, func(i, j int) bool {
		if len(reg.workspaces[i].Path) != len(reg.workspaces[j].Path) {
			return len(reg.workspaces[i].Path) > len(reg.workspaces[j].Path)
		}
		return reg.workspaces[i].Path < reg.workspaces[j].Path
	})
	return reg
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// sanitiseName makes one Drive folder segment out of free text: no path
// separators, no control characters, bounded, never empty, and never shaped
// like a legacy slug (which the migration would otherwise mistake for one).
func sanitiseName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			b.WriteByte('-')
		case unicode.IsControl(r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(strings.TrimSpace(b.String()), ".")
	if out == "" {
		out = "Untitled"
	}
	if len(out) > maxNameLen {
		out = out[:maxNameLen]
	}
	if isLegacySlug(out) {
		out = "Workspace " + out
	}
	return out
}

// registry loads the current registry, once per mirror pass.
func (s *Syncer) registry() registry {
	s.mu.Lock()
	file := s.registryFile
	s.mu.Unlock()
	return loadRegistry(file)
}

// SetRegistryFile names dsh's workspace registry document for this holder
// (`<dsh home>/storages/workspace.json`).
func (s *Syncer) SetRegistryFile(file string) {
	s.mu.Lock()
	s.registryFile = file
	s.mu.Unlock()
}

// displayFor names the Drive folder for a session's working directory: the
// workspace title when the registry knows the path (plus the sub-path when
// the session ran deeper), the path relative to the workspace root when it
// does not, and dsh's own key for a directory outside the root.
func (s *Syncer) displayFor(reg registry, cwd string) string {
	if cwd == "" || cwd == "." {
		return noWorkspaceName
	}
	cwd = filepath.Clean(cwd)
	for _, w := range reg.workspaces {
		if cwd == w.Path {
			return w.Display
		}
		if strings.HasPrefix(cwd, w.Path+string(filepath.Separator)) {
			return w.Display + "/" + sanitisePath(cwd[len(w.Path)+1:])
		}
	}
	root := filepath.Clean(s.workspace)
	if root != "" && root != "." && strings.HasPrefix(cwd, root+string(filepath.Separator)) {
		return sanitisePath(cwd[len(root)+1:])
	}
	// Outside the root: dsh's own rendering, minus the `--…--` wrapper that
	// would make the migration read the folder as a legacy one.
	return sanitiseName(strings.Trim(projectKey(cwd), "-"))
}

// sanitisePath sanitises each segment of a relative path, keeping the
// nesting.
func sanitisePath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	out := parts[:0]
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		out = append(out, sanitiseName(p))
	}
	if len(out) == 0 {
		return "Untitled"
	}
	return strings.Join(out, "/")
}

// remoteRel maps one local session file to its Drive path (slash-separated,
// under `sessions/`). A file whose session header cannot be read keeps its
// local directory name, so nothing is ever left behind for want of a name.
func (s *Syncer) remoteRel(reg registry, localPath string) string {
	rel, err := filepath.Rel(s.sessions, localPath)
	if err != nil {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 3 {
		// Not `<project>/<session>/<file>`: mirror it where it is.
		return sessionsFolder + "/" + strings.Join(parts, "/")
	}
	sessionDir := filepath.Join(s.sessions, parts[0], parts[1])
	meta, ok := s.sessionDirMeta(sessionDir)
	prefix := sessionsFolder + "/"
	display := parts[0]
	if ok {
		if reg.archived[meta.ID] {
			prefix += archivedFolder + "/"
		}
		display = s.displayFor(reg, meta.Cwd)
	} else if parts[0] == noCwdProjectDir {
		display = noWorkspaceName
	}
	return prefix + display + "/" + strings.Join(parts[1:], "/")
}

// ---- mirror -------------------------------------------------------------------

// mirrorSessions uploads every changed session file under its workspace
// name, deletes what no longer exists locally, and folds legacy slug
// folders into the new layout.
func (s *Syncer) mirrorSessions(ds *DriveStore) error {
	reg := s.registry()
	expected := map[string]bool{}
	var changed, failed int
	var refused error
	walkErr := filepath.WalkDir(s.sessions, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry must not abort the whole mirror: the rest of
			// the holder's sessions still deserve to reach their Drive.
			return nil
		}
		if d.IsDir() || isLockFile(d.Name()) {
			return nil
		}
		rel := s.remoteRel(reg, p)
		if rel == "" {
			return nil
		}
		expected[rel] = true
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		hash := contentHash(data)
		s.mu.Lock()
		unchanged := s.uploaded[rel] == hash
		s.mu.Unlock()
		if unchanged {
			return nil
		}
		parent, folderErr := s.ensurePath(ds, path.Dir(rel))
		if folderErr != nil {
			failed++
			log.Printf("[sync] folder for %s: %v", rel, folderErr)
			return nil
		}
		if _, putErr := ds.PutIn(parent, path.Base(rel), data); putErr != nil {
			failed++
			log.Printf("[sync] upload %s: %v", rel, putErr)
			if IsRefused(putErr) {
				refused = putErr
				return fs.SkipAll // the capability is gone; the rest would fail the same way
			}
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
	if refused != nil {
		return refused
	}
	if failed > 0 {
		return fmt.Errorf("capability: %d session file(s) could not be mirrored", failed)
	}
	// Only once everything local is on Drive under its current name may the
	// stale copies go: a move (archive, rename, migration) is upload first,
	// delete second, so no tick ever leaves a session with no copy at all.
	s.pruneRemote(ds, expected)
	s.migrateLegacy(ds)
	return nil
}

// pruneRemote deletes the Drive files the mirror uploaded that no local file
// maps to any more, then the folders that emptied. Refused deletes (the
// grant carries no `delete` yet) are kept for a later pass and reported once.
func (s *Syncer) pruneRemote(ds *DriveStore, expected map[string]bool) {
	s.mu.Lock()
	var stale []string
	for rel := range s.uploaded {
		if !expected[rel] {
			stale = append(stale, rel)
		}
	}
	s.mu.Unlock()
	if len(stale) == 0 {
		return
	}
	sort.Strings(stale)
	touched := map[string]bool{}
	deleted := 0
	for _, rel := range stale {
		node, found, err := ds.ResolvePath(ds.RootID(), rel)
		if err != nil {
			log.Printf("[sync] prune %s: %v", rel, err)
			continue
		}
		if found {
			if err := ds.Delete(node.ID); err != nil {
				if IsRefused(err) {
					s.setDeleteRefused(true, len(stale)-deleted)
					return
				}
				log.Printf("[sync] prune %s: %v", rel, err)
				continue
			}
		}
		s.mu.Lock()
		delete(s.uploaded, rel)
		s.mu.Unlock()
		touched[path.Dir(rel)] = true
		deleted++
	}
	s.setDeleteRefused(false, 0)
	if deleted > 0 {
		log.Printf("[sync] removed %d stale session file(s) from the holder's Drive", deleted)
	}
	s.pruneEmptyFolders(ds, touched)
}

// pruneEmptyFolders removes emptied folders under `sessions/`, deepest
// first, stopping at the first one that still holds something.
func (s *Syncer) pruneEmptyFolders(ds *DriveStore, dirs map[string]bool) {
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) })
	done := map[string]bool{}
	for _, dir := range list {
		for dir != "" && dir != "." && dir != sessionsFolder && !done[dir] {
			done[dir] = true
			node, found, err := ds.ResolvePath(ds.RootID(), dir)
			if err != nil || !found || !node.IsFolder() {
				break
			}
			children, err := ds.ListIn(node.ID)
			if err != nil || len(children) > 0 {
				break
			}
			if err := ds.Delete(node.ID); err != nil {
				if !IsRefused(err) {
					log.Printf("[sync] prune folder %s: %v", dir, err)
				}
				break
			}
			s.mu.Lock()
			delete(s.folders, dir)
			s.mu.Unlock()
			dir = path.Dir(dir)
		}
	}
}

// setDeleteRefused records whether Drive refused a delete this pass.
func (s *Syncer) setDeleteRefused(refused bool, pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if refused && !s.deleteRefused {
		log.Printf("[sync] Drive has not granted this harness `delete`: %d stale session file(s) stay until the holder approves it", pending)
	}
	s.deleteRefused = refused
	s.pendingDeletes = pending
}

// migrateLegacy folds the pre-2026-09-11 layout (`sessions/--slug--/…`) into
// the workspace-named one. A legacy session not present locally is restored
// first (the next pass uploads it under its workspace); once every session a
// legacy folder holds exists locally, the folder is deleted. Nothing is
// removed that has not been re-homed.
func (s *Syncer) migrateLegacy(ds *DriveStore) {
	sessionsID, err := s.folderIfExists(ds, sessionsFolder)
	if err != nil || sessionsID == "" {
		return
	}
	children, err := ds.ListIn(sessionsID)
	if err != nil {
		return
	}
	var local map[string]bool
	for _, c := range children {
		if !c.IsFolder() || !isLegacySlug(c.Name) {
			continue
		}
		if local == nil {
			local = s.localSessionDirs()
		}
		subs, err := ds.ListIn(c.ID)
		if err != nil {
			continue
		}
		complete := true
		for _, sub := range subs {
			if !sub.IsFolder() {
				continue
			}
			if local[sub.Name] {
				continue
			}
			complete = false
			if _, rerr := s.restoreSessionFolder(ds, sub, c.Name, sessionsFolder+"/"+c.Name+"/"+sub.Name); rerr != nil {
				log.Printf("[sync] migrate %s/%s: %v", c.Name, sub.Name, rerr)
			} else {
				log.Printf("[sync] legacy session %s/%s restored for re-homing", c.Name, sub.Name)
			}
		}
		if !complete {
			continue
		}
		if err := ds.Delete(c.ID); err != nil {
			if IsRefused(err) {
				s.setDeleteRefused(true, 1)
				return
			}
			log.Printf("[sync] migrate %s: %v", c.Name, err)
			continue
		}
		s.mu.Lock()
		for dir := range s.folders {
			if dir == sessionsFolder+"/"+c.Name || strings.HasPrefix(dir, sessionsFolder+"/"+c.Name+"/") {
				delete(s.folders, dir)
			}
		}
		s.mu.Unlock()
		log.Printf("[sync] legacy folder %s folded into the workspace layout", c.Name)
	}
}

// localSessionDirs is the set of session directory names under every local
// project directory.
func (s *Syncer) localSessionDirs() map[string]bool {
	out := map[string]bool{}
	projects, err := os.ReadDir(s.sessions)
	if err != nil {
		return out
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		subs, err := os.ReadDir(filepath.Join(s.sessions, p.Name()))
		if err != nil {
			continue
		}
		for _, sub := range subs {
			if sub.IsDir() {
				out[sub.Name()] = true
			}
		}
	}
	return out
}

// ---- restore ------------------------------------------------------------------

// restoreSessions brings every session under the Drive `sessions/` folder
// back to the directory dsh expects, whatever the folder was called on
// Drive: a workspace name, `Archived/…`, or a legacy slug.
func (s *Syncer) restoreSessions(ds *DriveStore, nodeID string) (int, error) {
	return s.restoreSessionTree(ds, nodeID, "", sessionsFolder)
}

// restoreSessionTree walks one Drive folder: a folder holding a session log
// is a session and is re-homed by its header; any other folder is walked.
func (s *Syncer) restoreSessionTree(ds *DriveStore, nodeID, parentName, remoteDir string) (int, error) {
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, err
	}
	count := 0
	var firstErr error
	for _, c := range children {
		if !c.IsFolder() {
			continue
		}
		subs, err := ds.ListIn(c.ID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		isSession := false
		for _, sub := range subs {
			if !sub.IsFolder() && isSessionLog(sub.Name) {
				isSession = true
				break
			}
		}
		var n int
		if isSession {
			n, err = s.restoreSessionFolder(ds, c, parentName, remoteDir+"/"+c.Name)
		} else {
			n, err = s.restoreSessionTree(ds, c.ID, c.Name, remoteDir+"/"+c.Name)
		}
		count += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return count, firstErr
}

// restoreSessionFolder downloads one session folder into a staging
// directory, reads its header, and moves it under the project directory dsh
// derives from the header's cwd. A session already present locally is left
// alone: the local copy is the live one. The uploaded-hash map is seeded
// with the Drive path each file was READ FROM (remoteDir), never with the
// path the mirror would choose today: a legacy or archived copy must count
// as stale until the mirror has written it under its current name, or the
// migration would delete the only remote copy.
func (s *Syncer) restoreSessionFolder(ds *DriveStore, node Node, parentName, remoteDir string) (int, error) {
	staging, err := os.MkdirTemp(filepath.Dir(s.sessions), ".restore-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(staging)
	n, err := s.restoreTree(ds, node.ID, staging)
	if err != nil {
		return 0, err
	}
	project := noCwdProjectDir
	if meta, ok := s.sessionDirMeta(staging); ok {
		if meta.Cwd != "" && meta.Cwd != "." {
			project = projectKey(meta.Cwd)
		}
	} else if isLegacySlug(parentName) {
		project = parentName
	}
	s.mu.Lock()
	delete(s.metaCache, staging)
	s.mu.Unlock()
	target := filepath.Join(s.sessions, project, node.Name)
	if _, statErr := os.Stat(target); statErr == nil {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return 0, err
	}
	if err := os.Rename(staging, target); err != nil {
		return 0, err
	}
	_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || isLockFile(d.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(target, p)
		data, readErr := os.ReadFile(p)
		if relErr != nil || readErr != nil {
			return nil
		}
		s.mu.Lock()
		s.uploaded[remoteDir+"/"+filepath.ToSlash(rel)] = contentHash(data)
		s.mu.Unlock()
		return nil
	})
	return n, nil
}
