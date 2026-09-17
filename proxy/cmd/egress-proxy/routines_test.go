// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

func feedAgent() capability.AgentSpec {
	var a capability.AgentSpec
	a.Name, a.Path = "Inbox triage", "/data/users/k/workspace/Inbox triage"
	a.Trigger.On = "mail.changes"
	a.Debounce, a.MinInterval = "2m", "10m"
	return a
}

func TestEventSourceIsToolDotCall(t *testing.T) {
	tool, call, ok := eventSource("mail.changes")
	if !ok || tool != "mail" || call != "changes" {
		t.Fatalf("mail.changes -> %q %q %v", tool, call, ok)
	}
	for _, bad := range []string{"", "mail", ".changes", "mail.", "a.b.c"} {
		if _, _, ok := eventSource(bad); ok {
			t.Errorf("%q must not be an event source", bad)
		}
	}
}

func TestPollBackoffDoublesToAQuarterHour(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		if got := pollBackoff(i + 1); got != w {
			t.Fatalf("failure %d: %s, want %s", i+1, got, w)
		}
	}
}

func TestEventRunWaitsForTheBurstToSettle(t *testing.T) {
	a := feedAgent()
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	if runDue(a, now, time.Time{}, now.Add(-time.Minute)) {
		t.Fatal("a burst one minute old must not run yet (debounce 2m)")
	}
	if !runDue(a, now, time.Time{}, now.Add(-3*time.Minute)) {
		t.Fatal("a burst that settled for the debounce must run")
	}
	if runDue(a, now, time.Time{}, time.Time{}) {
		t.Fatal("no events, no run")
	}
}

func TestMinimumIntervalSpacesRunsWhateverArrives(t *testing.T) {
	a := feedAgent()
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	if runDue(a, now, now.Add(-5*time.Minute), now.Add(-4*time.Minute)) {
		t.Fatal("five minutes after a run nothing may start (min_interval 10m)")
	}
	if !runDue(a, now, now.Add(-11*time.Minute), now.Add(-4*time.Minute)) {
		t.Fatal("past the interval the settled burst runs")
	}
}

func TestTimerRunsOnItsPeriodAndNotBefore(t *testing.T) {
	var a capability.AgentSpec
	a.Name = "Weekly digest"
	a.Trigger.Every = "2h"
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	if !runDue(a, now, time.Time{}, time.Time{}) {
		t.Fatal("a timer agent that never ran runs now")
	}
	if runDue(a, now, now.Add(-90*time.Minute), time.Time{}) {
		t.Fatal("ninety minutes into a two-hour period is not due")
	}
	if !runDue(a, now, now.Add(-121*time.Minute), time.Time{}) {
		t.Fatal("past the period it is due")
	}
	a.Paused = true
	if a.Scheduled() {
		t.Fatal("a paused agent is never scheduled")
	}
}

func TestReconcilePollsOnlyMountedSourcesAndDispatchesOnce(t *testing.T) {
	// No worker manager is needed for this path: the agent's source is not
	// mounted, so no poll starts, and nothing is due.
	e := newRoutineEngine(nil, nil, map[string]string{"drive": "drive.example"})
	st := e.fresh(&routineState{Subject: "sub-1", Agents: []capability.AgentSpec{feedAgent()}})
	e.mu.Lock()
	e.subjects["sub-1"] = st
	e.mu.Unlock()
	e.reconcile(t.Context(), st)
	if _, held := st.pollers["Inbox triage"]; !held {
		t.Fatal("an unmounted source is recorded so it is stated once, not every tick")
	}
	if st.dispatching["Inbox triage"] {
		t.Fatal("nothing was due")
	}
}

func TestRunPromptSaysWhyItStarted(t *testing.T) {
	a := feedAgent()
	title, prompt := runTitleAndPrompt(a, time.Date(2026, 9, 14, 9, 20, 0, 0, time.UTC))
	if title != "Inbox triage 2026-09-14 09:20" {
		t.Fatalf("title %q", title)
	}
	if !strings.Contains(prompt, "mail.changes") || !strings.Contains(prompt, "unattended") {
		t.Fatalf("prompt must name the trigger and that nobody is watching: %q", prompt)
	}
}
