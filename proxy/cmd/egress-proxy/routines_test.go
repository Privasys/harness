// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
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

func TestHolderNeededIsAForbiddenWithTheField(t *testing.T) {
	if !holderNeeded(403, []byte(`{"error":"no account for this holder","credential_needed":true,"needs_holder":true}`)) {
		t.Fatal("a 403 saying needs_holder is the tool waiting for the holder")
	}
	if holderNeeded(403, []byte(`{"error":"forbidden"}`)) || holderNeeded(200, []byte(`{"needs_holder":true}`)) || holderNeeded(403, []byte(`not json`)) {
		t.Fatal("any other refusal is an ordinary failure")
	}
}

// holdingEngine is an engine whose feed and device are fakes: the feed
// records every call and answers "needs the holder" until told otherwise,
// then parks like a quiet source; the device records every ask.
type holdingEngine struct {
	*routineEngine
	calls, asks chan string
	needs       atomic.Bool
}

func newHoldingEngine(resource string) *holdingEngine {
	h := &holdingEngine{routineEngine: newRoutineEngine(nil, nil, map[string]string{"mail": "mail.example"}),
		calls: make(chan string, 16), asks: make(chan string, 16)}
	h.needs.Store(true)
	h.feed = func(ctx context.Context, sub, tool, call, cursor string) (int, string, error) {
		h.calls <- tool + "." + call
		if h.needs.Load() {
			return 0, "", fmt.Errorf("%w: %s", errNeedsHolder, `{"error":"no account for this holder","needs_holder":true}`)
		}
		<-ctx.Done()
		return 0, "", ctx.Err()
	}
	h.resourceFor = func(sub, tool string) string { return resource }
	h.askHolder = func(sub, res string) error { h.asks <- res; return nil }
	return h
}

func (h *holdingEngine) expectQuiet(t *testing.T, why string) {
	t.Helper()
	select {
	case c := <-h.calls:
		t.Fatalf("%s: the feed was called (%s)", why, c)
	case a := <-h.asks:
		t.Fatalf("%s: the device was asked (%s)", why, a)
	case <-time.After(150 * time.Millisecond):
	}
}

func (h *holdingEngine) expectCall(t *testing.T, why string) {
	t.Helper()
	select {
	case <-h.calls:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: the feed was not called", why)
	}
}

func TestFeedThatNeedsTheHolderAsksOnceAndHoldsForTheApproval(t *testing.T) {
	h := newHoldingEngine("mailbox")
	st := h.fresh(&routineState{Subject: "sub-1"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.poll(ctx, st, feedAgent())

	h.expectCall(t, "the first call")
	select {
	case res := <-h.asks:
		if res != "mailbox" {
			t.Fatalf("asked for %q, want the resource the tool serves", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the holder's device was not asked")
	}
	h.expectQuiet(t, "while the holder is asked")
	if !h.waiting("sub-1", "Inbox triage") {
		t.Fatal("the feed is held for the holder")
	}

	// The holder said no: said once, still held, and never asked again.
	h.Event(capability.Event{Type: "capability.denied", Resource: "mailbox", Subject: "sub-1"})
	h.Event(capability.Event{Type: "capability.denied", Resource: "mailbox", Subject: "sub-1"})
	h.expectQuiet(t, "after a denial")
	// Another resource's approval, or another holder's, releases nothing.
	h.Event(capability.Event{Type: "capability.approved", Resource: "storage", Subject: "sub-1"})
	h.Event(capability.Event{Type: "capability.approved", Resource: "mailbox", Subject: "sub-2"})
	h.expectQuiet(t, "after unrelated approvals")

	// The approval releases the feed, which answers now.
	h.needs.Store(false)
	h.Event(capability.Event{Type: "capability.approved", Resource: "mailbox", Subject: "sub-1"})
	h.expectCall(t, "after the approval")
	if h.waiting("sub-1", "Inbox triage") {
		t.Fatal("nothing is held once the holder approved")
	}
	if len(h.asks) != 0 {
		t.Fatal("one refusal episode is one ask")
	}
}

func TestFeedWithNoDeclaredResourceHoldsForAnyApproval(t *testing.T) {
	h := newHoldingEngine("")
	st := h.fresh(&routineState{Subject: "sub-1"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.poll(ctx, st, feedAgent())

	h.expectCall(t, "the first call")
	h.expectQuiet(t, "with nothing to ask for")
	h.Event(capability.Event{Type: "capability.denied", Resource: "mailbox", Subject: "sub-1"})
	h.expectQuiet(t, "after a denial")
	h.needs.Store(false)
	h.Event(capability.Event{Type: "capability.approved", Resource: "storage", Subject: "sub-1"})
	h.expectCall(t, "after any approval of the holder's")
}

func TestARemovedAgentEndsItsHold(t *testing.T) {
	h := newHoldingEngine("mailbox")
	st := h.fresh(&routineState{Subject: "sub-1"})
	ctx, cancel := context.WithCancel(t.Context())
	go h.poll(ctx, st, feedAgent())
	h.expectCall(t, "the first call")
	<-h.asks
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for h.waiting("sub-1", "Inbox triage") {
		if time.Now().After(deadline) {
			t.Fatal("the hold outlived the agent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
