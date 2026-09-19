// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"os"
	"path/filepath"
	"strings"
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

// The agents are local files: written by the chat through WriteAgent, listed
// from the marker, and a session run inside one is an agent run, not a
// conversation for the Drive.
func TestAgentsAreLocalFilesAndTheirRunsStayHome(t *testing.T) {
	root := t.TempDir()
	s := &Syncer{}
	s.SetAgentsRoot(root, 0)
	if got := s.Agents(); len(got) != 0 {
		t.Fatalf("no agents yet: %+v", got)
	}
	rel, err := s.WriteAgent("Inbox triage", map[string][]byte{
		agentPersonaFile: []byte("# Inbox triage\n"),
		agentSpecFile:    []byte("prompt: triage\ntrigger:\n  on: mail.changes\n"),
	})
	if err != nil || rel != "Inbox triage" {
		t.Fatalf("write: %q %v", rel, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Inbox triage", agentMarkerFile)); err != nil {
		t.Fatal("the marker names the directory as an agent's")
	}
	for _, d := range []string{"state", "runs"} {
		if fi, err := os.Stat(filepath.Join(root, "Inbox triage", d)); err != nil || !fi.IsDir() {
			t.Fatalf("output folder %s must exist", d)
		}
	}
	agents := s.Agents()
	if len(agents) != 1 || agents[0].Name != "Inbox triage" || agents[0].Trigger.On != "mail.changes" || !agents[0].Scheduled() {
		t.Fatalf("listed: %+v", agents)
	}
	if !s.isAgentDir("Inbox triage") || s.isAgentDir("Notes") {
		t.Fatal("only the marked directory is an agent's")
	}
	if !s.isAgentCwd(filepath.Join(root, "Inbox triage")) || !s.isAgentCwd(filepath.Join(root, "Inbox triage", "runs", "x")) {
		t.Fatal("a run in the agent's workspace is an agent run")
	}
	if s.isAgentCwd(filepath.Join(root, "Notes")) || s.isAgentCwd(root) || s.isAgentCwd(filepath.Dir(root)) {
		t.Fatal("anything else is a conversation")
	}
	// Writing the same bytes again changes nothing; a bad file name is refused.
	if _, err := s.WriteAgent("Inbox triage", map[string][]byte{agentPersonaFile: []byte("# Inbox triage\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteAgent("Inbox triage", map[string][]byte{"../escape": []byte("x")}); err == nil {
		t.Fatal("a file name that leaves the agent directory must be refused")
	}
}

func TestAgentSpecReadsACronScheduleInUTC(t *testing.T) {
	spec, err := parseAgentSpec([]byte("prompt: Send the weekly digest.\ntrigger:\n  at: \"0 17 * * FRI\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Scheduled() || spec.Schedule() == nil {
		t.Fatalf("a schedule is a trigger: %+v", spec)
	}
	from := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC) // a Monday
	if next := spec.Schedule().Next(from); next != time.Date(2026, 9, 18, 17, 0, 0, 0, time.UTC) {
		t.Fatalf("next after a Monday morning is Friday 17:00 UTC, got %s", next)
	}
	daily, err := parseAgentSpec([]byte("trigger:\n  at: \"@daily\"\n"))
	if err != nil || daily.Schedule() == nil {
		t.Fatalf("descriptors are schedules too: %v", err)
	}
	for _, bad := range []string{"trigger:\n  at: \"0 17 * *\"\n", "trigger:\n  at: \"every friday\"\n", "trigger:\n  at: \"0 0 0 17 * * FRI\"\n"} {
		if _, err := parseAgentSpec([]byte(bad)); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestAgentSpecHasExactlyOneTrigger(t *testing.T) {
	for _, two := range []string{
		"trigger:\n  every: 2h\n  at: \"@daily\"\n",
		"trigger:\n  every: 2h\n  on: mail.changes\n",
		"trigger:\n  at: \"@daily\"\n  on: mail.changes\n",
	} {
		_, err := parseAgentSpec([]byte(two))
		if err == nil || !strings.Contains(err.Error(), "exactly one trigger") {
			t.Errorf("%q: want the one-trigger error, got %v", two, err)
		}
	}
	none, err := parseAgentSpec([]byte("prompt: only when asked\n"))
	if err != nil || none.Scheduled() {
		t.Fatalf("no trigger is a valid agent that runs when asked: %v %+v", err, none)
	}
}
