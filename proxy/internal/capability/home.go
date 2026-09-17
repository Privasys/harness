// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The holder's dsh home: what they set in Settings, how they named and
// grouped their workspaces, and the files they attached to a message. It is
// theirs like a session is, so it lives in their Drive under `home/` and
// this enclave keeps no durable copy: the local home is a scratch that a
// restart or an idle stop wipes, brought back here before dsh starts.
//
// Only what the holder made is carried. The rest of a dsh home is either the
// measured profile (refreshed from the image at every start) or a cache dsh
// rebuilds (an upload index, an anonymous id), and neither belongs in
// anybody's Drive.
const homeFolder = "home"

// homeKept lists what is carried, as paths relative to the dsh home. A
// trailing slash is a whole tree; a `*` matches one path segment.
var homeKept = []string{
	"settings.yaml",
	"storages/",
	"cache/attachments/",
	"profiles/*/cordis.patch.yml",
}

func homeCarries(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, k := range homeKept {
		if strings.HasSuffix(k, "/") {
			if strings.HasPrefix(rel, k) {
				return true
			}
			continue
		}
		if ok, _ := path.Match(k, rel); ok {
			return true
		}
	}
	return false
}

// SetHomeRoot names this holder's dsh home.
func (s *Syncer) SetHomeRoot(dir string) {
	s.mu.Lock()
	s.home = dir
	s.mu.Unlock()
}

func (s *Syncer) homeRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.home
}

// mirrorHome uploads what changed since the last pass. Nothing is ever
// deleted from the Drive copy: a setting file that vanished locally is a
// wiped scratch, not a decision of the holder's.
func (s *Syncer) mirrorHome(ds *DriveStore) error {
	root := s.homeRoot()
	if root == "" {
		return nil
	}
	var firstErr error
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Descend only towards something carried: the profile tree is
			// large and none of it but the user layer is the holder's.
			if homeCarries(rel+"/") || homeLeadsToKept(rel) {
				return nil
			}
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() || !homeCarries(rel) || isLockFile(d.Name()) {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil || len(data) > maxSnapshotFile {
			return nil
		}
		key := homeFolder + "/" + rel
		sum := contentHash(data)
		s.mu.Lock()
		same := s.uploaded[key] == sum
		s.mu.Unlock()
		if same {
			return nil
		}
		parent, perr := s.ensurePath(ds, path.Dir(key))
		if perr == nil {
			_, perr = ds.PutIn(parent, d.Name(), data)
		}
		if perr != nil {
			if firstErr == nil {
				firstErr = perr
			}
			if IsRefused(perr) {
				return fs.SkipAll
			}
			return nil
		}
		s.mu.Lock()
		s.uploaded[key] = sum
		s.mu.Unlock()
		return nil
	})
	return firstErr
}

// homeLeadsToKept reports whether a directory is on the way to a kept path.
func homeLeadsToKept(relDir string) bool {
	segs := strings.Split(relDir, "/")
	for _, k := range homeKept {
		ks := strings.Split(strings.TrimSuffix(k, "/"), "/")
		if len(segs) >= len(ks) {
			continue
		}
		match := true
		for i, seg := range segs {
			if ok, _ := path.Match(ks[i], seg); !ok {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// restoreHome brings the holder's home back before dsh starts. It writes
// only carried paths, and never over a file that is already there: within
// one container life the local home is the newer of the two.
func (s *Syncer) restoreHome(ds *DriveStore) (int, error) {
	root := s.homeRoot()
	if root == "" {
		return 0, nil
	}
	id, err := s.folderIfExists(ds, homeFolder)
	if err != nil || id == "" {
		return 0, err
	}
	return s.restoreHomeTree(ds, id, "")
}

func (s *Syncer) restoreHomeTree(ds *DriveStore, nodeID, rel string) (int, error) {
	children, err := ds.ListIn(nodeID)
	if err != nil {
		return 0, err
	}
	root := s.homeRoot()
	count := 0
	for _, c := range children {
		childRel := strings.TrimPrefix(rel+"/"+c.Name, "/")
		if c.IsFolder() {
			n, err := s.restoreHomeTree(ds, c.ID, childRel)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		// The Drive folder is the holder's and they can put anything in it;
		// only a carried path is ever written into the home.
		if !homeCarries(childRel) || strings.Contains(childRel, "..") {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(childRel))
		if _, statErr := os.Stat(target); statErr == nil {
			continue
		}
		data, err := ds.Get(c.ID)
		if err != nil {
			log.Printf("[sync] restore home %s: %v", childRel, err)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return count, err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return count, err
		}
		s.mu.Lock()
		s.uploaded[homeFolder+"/"+childRel] = contentHash(data)
		s.mu.Unlock()
		count++
	}
	return count, nil
}

// Flush mirrors everything and reports whether the holder's Drive now holds
// all of it, which is what makes wiping the local scratch safe. The
// workspace snapshot waits for the tree to hold still for one pass, so a
// stopped worker needs two; anything short of a clean, complete mirror
// answers false and the caller keeps the files.
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
	// SyncOnce logs a failure of these two and carries on; here it decides.
	ds, _ := s.store()
	if ds == nil || s.mirrorHome(ds) != nil || s.syncAgents(ds) != nil {
		return false
	}
	files, dirs, _, tree := s.scanWorkspace()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.passRefused || s.lastErr != "" {
		return false
	}
	return (len(files) == 0 && len(dirs) == 0) || s.treeSaved == tree
}
