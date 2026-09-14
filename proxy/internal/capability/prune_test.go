// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"sort"
	"strings"
	"testing"
)

// staleUploads is the selection pruneRemote makes, factored for the test:
// only session files the local walk no longer expects.
func staleUploads(uploaded map[string]string, expected map[string]bool) []string {
	var stale []string
	for rel := range uploaded {
		if !expected[rel] && strings.HasPrefix(rel, sessionsFolder+"/") {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	return stale
}

func TestPruneNeverTouchesSkillsOrAgents(t *testing.T) {
	uploaded := map[string]string{
		"sessions/Chat/session-1/session.v3.jsonl.zstd": "a",
		"sessions/Chat/session-2/session.v3.jsonl.zstd": "b", // deleted locally
		"skills/inbox-triage/SKILL.md":                   "c",
		"agents/Inbox triage/agent.md":                   "d",
		"agents/Inbox triage/state/triage.jsonl":         "e",
	}
	expected := map[string]bool{"sessions/Chat/session-1/session.v3.jsonl.zstd": true}
	got := staleUploads(uploaded, expected)
	if len(got) != 1 || got[0] != "sessions/Chat/session-2/session.v3.jsonl.zstd" {
		t.Fatalf("stale = %v, want only the deleted session file", got)
	}
}
