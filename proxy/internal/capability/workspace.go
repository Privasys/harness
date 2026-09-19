// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceOfSession resolves the workspace one session belongs to, from
// dsh's own workspace registry (registryFile) and, when membership has not
// caught up yet, from the session log's working directory under
// sessionsRoot matched against the workspace paths. "" when the session is
// ungrouped or unknown. Read fresh on every call: the document is small and
// the registry changes as the user works.
func WorkspaceOfSession(registryFile, sessionsRoot, sessionID string) string {
	if registryFile == "" || sessionID == "" {
		return ""
	}
	raw, err := os.ReadFile(registryFile)
	if err != nil {
		return ""
	}
	var doc struct {
		Tables struct {
			Workspaces map[string]struct {
				Path       string   `json:"path"`
				SessionIds []string `json:"sessionIds"`
			} `json:"workspaces"`
		} `json:"tables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for id, w := range doc.Tables.Workspaces {
		for _, s := range w.SessionIds {
			if s == sessionID {
				return id
			}
		}
	}
	// Not accounted yet: a session created moments ago. Its log names its
	// working directory, and a workspace owns that directory.
	cwd := sessionCwdByID(sessionsRoot, sessionID)
	if cwd == "" {
		return ""
	}
	cwd = filepath.Clean(cwd)
	best, bestLen := "", 0
	for id, w := range doc.Tables.Workspaces {
		p := filepath.Clean(w.Path)
		if p == "" || p == "." {
			continue
		}
		if cwd == p || strings.HasPrefix(cwd, p+string(filepath.Separator)) {
			if len(p) > bestLen {
				best, bestLen = id, len(p)
			}
		}
	}
	return best
}

// sessionCwdByID finds a session's log under any project directory and
// returns the cwd its header names.
func sessionCwdByID(sessionsRoot, sessionID string) string {
	if sessionsRoot == "" {
		return ""
	}
	projects, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return ""
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsRoot, p.Name(), sessionID)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isSessionLog(e.Name()) {
				continue
			}
			if m, ok := readSessionMeta(filepath.Join(dir, e.Name())); ok && m.Cwd != "." {
				return m.Cwd
			}
		}
	}
	return ""
}

// WorkspacePathByID returns the directory dsh's registry records for a
// workspace id, or "".
func WorkspacePathByID(registryFile, workspaceID string) string {
	raw, err := os.ReadFile(registryFile)
	if err != nil {
		return ""
	}
	var doc struct {
		Tables struct {
			Workspaces map[string]struct {
				Path string `json:"path"`
			} `json:"workspaces"`
		} `json:"tables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	if w, ok := doc.Tables.Workspaces[workspaceID]; ok && w.Path != "" {
		return filepath.Clean(w.Path)
	}
	return ""
}

// SessionDirsFor lists the session directories under sessionsRoot whose
// header names cwd as their working directory: the session logs of one
// workspace.
func SessionDirsFor(sessionsRoot, cwd string) []string {
	if sessionsRoot == "" || cwd == "" {
		return nil
	}
	cwd = filepath.Clean(cwd)
	var out []string
	projects, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return nil
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		sessions, err := os.ReadDir(filepath.Join(sessionsRoot, p.Name()))
		if err != nil {
			continue
		}
		for _, sdir := range sessions {
			if !sdir.IsDir() {
				continue
			}
			dir := filepath.Join(sessionsRoot, p.Name(), sdir.Name())
			logs, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, l := range logs {
				if l.IsDir() || !isSessionLog(l.Name()) {
					continue
				}
				if m, ok := readSessionMeta(filepath.Join(dir, l.Name())); ok {
					if filepath.Clean(m.Cwd) == cwd {
						out = append(out, dir)
					}
					break
				}
			}
		}
	}
	return out
}

// WorkspacePathsByID returns every workspace dsh's registry records, id to
// directory.
func WorkspacePathsByID(registryFile string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(registryFile)
	if err != nil {
		return out
	}
	var doc struct {
		Tables struct {
			Workspaces map[string]struct {
				Path string `json:"path"`
			} `json:"workspaces"`
		} `json:"tables"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for id, w := range doc.Tables.Workspaces {
		if w.Path != "" {
			out[id] = filepath.Clean(w.Path)
		}
	}
	return out
}
