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
