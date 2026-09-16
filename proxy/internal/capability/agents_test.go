// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentSpecParsesTriggerAndDefaults(t *testing.T) {
	spec, err := parseAgentSpec([]byte(`
prompt: Triage what arrived since the last run.
trigger:
  on: mail.changes
min_interval: 15m
`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Trigger.On != "mail.changes" || !spec.Scheduled() {
		t.Fatalf("parsed %+v", spec)
	}
	debounce, minInterval := spec.Durations()
	if debounce != 2*time.Minute || minInterval != 15*time.Minute {
		t.Fatalf("durations %v %v, want the default debounce and the declared interval", debounce, minInterval)
	}
}

func TestAgentSpecRejectsABadEveryAndAcceptsAnEmptyFile(t *testing.T) {
	if _, err := parseAgentSpec([]byte("trigger:\n  every: twice\n")); err == nil {
		t.Fatal("a trigger that is not a duration must be refused")
	}
	if _, err := parseAgentSpec([]byte("trigger:\n  on: changes\n")); err == nil {
		t.Fatal("an event source must name its tool: <tool>.<call>")
	}
	spec, err := parseAgentSpec([]byte("  \n"))
	if err != nil || spec.Scheduled() {
		t.Fatalf("an empty agent.yaml is an agent that runs only when asked: %+v %v", spec, err)
	}
	paused, _ := parseAgentSpec([]byte("trigger: {every: 2h}\npaused: true\n"))
	if paused.Scheduled() {
		t.Fatal("a paused agent is not scheduled")
	}
}

func TestAgentNamesAreOnePlainSegment(t *testing.T) {
	for _, ok := range []string{"Inbox triage", "weekly-digest", "Ops"} {
		if !validAgentName(ok) {
			t.Errorf("%q should be a valid agent name", ok)
		}
	}
	for _, bad := range []string{"", ".hidden", "a/b", `a\b`, " padded", ".."} {
		if validAgentName(bad) {
			t.Errorf("%q must not be an agent name", bad)
		}
	}
}

func TestPruneRemovesStaleDefinitionFilesAndKeepsOutputs(t *testing.T) {
	local := t.TempDir()
	keep := filepath.Join(local, "agent.md")
	stale := filepath.Join(local, ".agents", "skills", "old", "SKILL.md")
	out := filepath.Join(local, "state", "triage.jsonl")
	for _, p := range []string{keep, stale, out} {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	removed := pruneDefinition(local, map[string]bool{keep: true})
	if removed != 1 {
		t.Fatalf("removed %d, want the one stale skill file", removed)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("the stale skill file is still there")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal("a run's output was pruned; outputs are never touched by the pull")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a present file was pruned")
	}
}

func TestSnapshotLeavesAgentFoldersOut(t *testing.T) {
	ws := t.TempDir()
	_ = os.MkdirAll(filepath.Join(ws, "Chat"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "Chat", "notes.md"), []byte("n"), 0o644)
	_ = os.MkdirAll(filepath.Join(ws, "Inbox triage", "state"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "Inbox triage", "agent.md"), []byte("a"), 0o644)
	s := &Syncer{workspace: ws, uploaded: map[string]string{}, agentDirs: map[string]bool{"Inbox triage": true}}
	files, dirs, _, _ := s.scanWorkspace()
	if len(files) != 1 || files[0].Path != "Chat/notes.md" {
		t.Fatalf("files %+v, want only the ordinary workspace file", files)
	}
	for _, d := range dirs {
		if d == "Inbox triage" || d == "Inbox triage/state" {
			t.Fatalf("agent directory %q reached the snapshot", d)
		}
	}
}

// A directory the mirror made stays the mirror's across a restart (the
// marker), and a leftover holding only the output folders is never mistaken
// for someone's workspace (2026-09-16: a re-created agent was refused with
// "exists as a workspace that is not an agent" and never ran).
func TestAgentDirClaimsSurviveARestartAndLeftoversAreClaimable(t *testing.T) {
	root := t.TempDir()
	marked := filepath.Join(root, "Inbox triage")
	if err := os.MkdirAll(filepath.Join(marked, "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marked, agentMarkerFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(root, "Weekly")
	if err := os.MkdirAll(filepath.Join(leftover, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	theirs := filepath.Join(root, "Notes")
	if err := os.MkdirAll(theirs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(theirs, "todo.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Syncer{}
	s.SetAgentsRoot(root, 0)
	if !s.isAgentDir("Inbox triage") {
		t.Fatal("the marked directory must be the mirror's again after a restart")
	}
	if !s.claimAgentDir(leftover, "Weekly") {
		t.Fatal("a leftover with only output folders must be claimable")
	}
	if s.claimAgentDir(theirs, "Notes") {
		t.Fatal("a directory with the holder's own files must never be taken over")
	}
}
