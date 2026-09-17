// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Routines: unattended runs of the holder's agents.
//
// dsh has no scheduler and nothing that wakes a cold session, so unattended
// runs come from THIS proxy. Each agent folder in the holder's Drive carries
// an agent.yaml with a trigger (capability/agents.go); this engine owns the
// clock and the events, and is generic over both:
//
//   - `every: 2h`          a timer.
//   - `on: <tool>.<call>`  an EVENT SOURCE: one held long poll per holder and
//                          agent on the named tool's call, over the same
//                          attested tool leg the agent uses, as the holder.
//                          The tool must be one this deployment mounts
//                          (HARNESS_TOOLS) and the call must follow the
//                          change-feed contract below. The tool never calls
//                          in: the harness has no attested door and the tool
//                          stays the callee.
//
// The change-feed contract. The engine calls `<call>` with
//
//	{"since": "<cursor or empty>", "wait_seconds": N}
//
// and expects `{"changes": [...], "cursor": "<opaque>"}`. The tool holds the
// request for up to N seconds until something changes (a quiet source costs
// one held request a minute); a non-empty changes list opens or extends a
// burst, the cursor is remembered per agent. Any tool that answers this shape
// can drive an agent; the harness knows nothing about what the changes are.
//
// A run is dispatched through the worker's routines door (workers.go,
// app/privasys-routines.mjs): one POST on the worker's own loopback server,
// behind its ingress token, that dsh's webhook runtime turns into a new
// session in the agent's workspace, so the holder reads it where they read
// everything else. Bursts are debounced and runs are spaced by the agent's
// minimum interval, which is also the one-run-at-a-time guard: the door
// answers 202 at dispatch and never reports completion.
//
// Authority is durable without a browser: grants live on the manager, the
// worker is started from the subject string alone, and the agents a holder
// declared are remembered on the encrypted volume so the poll is held while
// they are away. A source that keeps failing (a withdrawn grant, a tool that
// is down) backs off to a quarter of an hour and says so once, not once a
// minute.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

const (
	routineTick     = 30 * time.Second
	routinePollWait = 60 // seconds a tool may hold one change-feed call
	// routineStartWait bounds how long a dispatch waits for a cold worker.
	routineStartWait = 3 * time.Minute
	// routinePollBackoffMax caps the wait between failed change-feed calls.
	routinePollBackoffMax = 15 * time.Minute
)

// eventSource splits `on: <tool>.<call>` into the tool that serves the feed
// and the call to hold. Anything else is not an event source.
func eventSource(on string) (tool, call string, ok bool) {
	tool, call, ok = strings.Cut(on, ".")
	if !ok || tool == "" || call == "" || strings.Contains(call, ".") {
		return "", "", false
	}
	return tool, call, true
}

// pollBackoff is the wait after the n-th consecutive failure (n >= 1): one
// minute, doubling, capped.
func pollBackoff(failures int) time.Duration {
	d := time.Minute
	for i := 1; i < failures && d < routinePollBackoffMax; i++ {
		d *= 2
	}
	if d > routinePollBackoffMax {
		d = routinePollBackoffMax
	}
	return d
}

// routineState is what the engine holds per holder, in MEMORY only: the
// harness keeps no record of a holder on its enclave. The agents are read
// from the holder's Drive by their mirror, the cursors start at "now" on a
// fresh process, and after a restart a holder's runs resume when they (or
// the runtime's list of approved subjects, once it exists) start the worker.
type routineState struct {
	Subject string                 `json:"subject"`
	Agents  []capability.AgentSpec `json:"agents"`
	// Cursors is the change-feed cursor per agent; LastRun the last dispatch.
	Cursors map[string]string    `json:"cursors"`
	LastRun map[string]time.Time `json:"last_run"`

	pending     map[string]time.Time          // agent -> first event of the current burst
	pollers     map[string]context.CancelFunc // agent -> the held poll
	dispatching map[string]bool               // agent -> a dispatch in flight
}

type routineEngine struct {
	mgr       *WorkerManager
	client    *http.Client
	toolHosts map[string]string
	now       func() time.Time

	mu       sync.Mutex
	subjects map[string]*routineState
}

func newRoutineEngine(mgr *WorkerManager, client *http.Client, toolHosts map[string]string) *routineEngine {
	return &routineEngine{mgr: mgr, client: client, toolHosts: toolHosts,
		now: time.Now, subjects: map[string]*routineState{}}
}

func (e *routineEngine) fresh(st *routineState) *routineState {
	if st.Cursors == nil {
		st.Cursors = map[string]string{}
	}
	if st.LastRun == nil {
		st.LastRun = map[string]time.Time{}
	}
	st.pending = map[string]time.Time{}
	st.pollers = map[string]context.CancelFunc{}
	st.dispatching = map[string]bool{}
	return st
}

// Start runs the tick loop until ctx ends.
func (e *routineEngine) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(routineTick)
		defer t.Stop()
		for {
			e.tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// tick refreshes each running worker's agents, arms or disarms polls, and
// dispatches whatever is due. Nothing here waits on a worker: a dispatch
// runs on its own goroutine, so one cold start never holds the others.
func (e *routineEngine) tick(ctx context.Context) {
	e.mgr.Each(func(w *Worker) {
		if w.Subject == systemSubject {
			return
		}
		if s := w.Syncer(); s != nil {
			e.update(w.Subject, s.Agents())
		}
	})
	e.mu.Lock()
	subs := make([]*routineState, 0, len(e.subjects))
	for _, st := range e.subjects {
		subs = append(subs, st)
	}
	e.mu.Unlock()
	for _, st := range subs {
		e.reconcile(ctx, st)
	}
}

// update replaces a holder's agents with what their mirror last read.
func (e *routineEngine) update(sub string, agents []capability.AgentSpec) {
	e.mu.Lock()
	st := e.subjects[sub]
	if st == nil {
		if len(agents) == 0 {
			e.mu.Unlock()
			return
		}
		st = e.fresh(&routineState{Subject: sub})
		e.subjects[sub] = st
	}
	st.Agents = agents
	e.mu.Unlock()
}

func sameAgents(a, b []capability.AgentSpec) bool {
	if len(a) != len(b) {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// reconcile arms one held poll per event-driven agent whose source this
// deployment mounts, cancels polls of agents no longer scheduled, and
// dispatches due runs.
func (e *routineEngine) reconcile(ctx context.Context, st *routineState) {
	now := e.now()
	e.mu.Lock()
	scheduled := map[string]capability.AgentSpec{}
	for _, a := range st.Agents {
		if a.Scheduled() {
			scheduled[a.Name] = a
		}
	}
	for name, cancel := range st.pollers {
		if a, ok := scheduled[name]; !ok || a.Trigger.On == "" {
			cancel()
			delete(st.pollers, name)
		}
	}
	var due []capability.AgentSpec
	for name, a := range scheduled {
		if tool, _, ok := eventSource(a.Trigger.On); ok && st.pollers[name] == nil {
			if e.toolHosts[tool] == "" {
				// Declared against a tool this deployment does not mount:
				// not an error to retry, a fact to state once per change.
				log.Printf("[routines] %.8s…/%s: %q names a tool this deployment does not mount; not polled", st.Subject, name, a.Trigger.On)
				st.pollers[name] = func() {}
			} else {
				pctx, cancel := context.WithCancel(ctx)
				st.pollers[name] = cancel
				go e.poll(pctx, st, a)
			}
		}
		if !st.dispatching[name] && runDue(a, now, st.LastRun[name], st.pending[name]) {
			due = append(due, a)
			st.dispatching[name] = true
		}
	}
	e.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].Name < due[j].Name })
	for _, a := range due {
		go e.dispatch(ctx, st, a)
	}
}

// runDue decides whether an agent runs now: a timer that elapsed, or a burst
// of events that settled for the debounce, and in both cases not before the
// minimum interval since the last run, which is also the one-at-a-time guard.
func runDue(a capability.AgentSpec, now, lastRun, pendingSince time.Time) bool {
	debounce, minInterval := a.Durations()
	if !lastRun.IsZero() && now.Sub(lastRun) < minInterval {
		return false
	}
	if a.Trigger.Every != "" {
		every, err := time.ParseDuration(a.Trigger.Every)
		if err == nil && every > 0 && (lastRun.IsZero() || now.Sub(lastRun) >= every) {
			return true
		}
	}
	if a.Trigger.On != "" && !pendingSince.IsZero() && now.Sub(pendingSince) >= debounce {
		return true
	}
	return false
}

// poll holds the source's change-feed call for one holder and agent, as the
// holder, over the attested tool leg; arrivals open or extend a burst.
func (e *routineEngine) poll(ctx context.Context, st *routineState, a capability.AgentSpec) {
	tool, call, _ := eventSource(a.Trigger.On)
	log.Printf("[routines] %.8s…/%s: holding the %s change feed", st.Subject, a.Name, a.Trigger.On)
	failures := 0
	for ctx.Err() == nil {
		e.mu.Lock()
		cursor := st.Cursors[a.Name]
		e.mu.Unlock()
		changes, next, err := e.changes(ctx, st.Subject, tool, call, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait := pollBackoff(failures)
			if failures == 1 || wait == routinePollBackoffMax && failures == 5 {
				log.Printf("[routines] %.8s…/%s: change feed: %v (retrying in %s%s)", st.Subject, a.Name, err, wait,
					map[bool]string{true: ", and quietly from now on", false: ""}[wait == routinePollBackoffMax])
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		if failures > 0 {
			log.Printf("[routines] %.8s…/%s: change feed answers again", st.Subject, a.Name)
			failures = 0
		}
		e.mu.Lock()
		if next != "" {
			st.Cursors[a.Name] = next
		}
		if changes > 0 && st.pending[a.Name].IsZero() {
			st.pending[a.Name] = e.now()
		}
		e.mu.Unlock()
	}
}

// changes makes one held change-feed call as the holder.
func (e *routineEngine) changes(ctx context.Context, sub, tool, call, cursor string) (int, string, error) {
	args, _ := json.Marshal(map[string]any{"since": cursor, "wait_seconds": routinePollWait})
	cctx, cancel := context.WithTimeout(ctx, (routinePollWait+30)*time.Second)
	defer cancel()
	carrier, _ := http.NewRequestWithContext(cctx, http.MethodPost, "/", nil)
	raw, status, _, err := callTool(carrier, e.client, e.toolHosts[tool], call, args, "", sub)
	if err != nil {
		return 0, "", err
	}
	if status/100 != 2 {
		return 0, "", fmt.Errorf("status %d: %s", status, truncate(raw, 200))
	}
	var out struct {
		Changes []json.RawMessage `json:"changes"`
		Cursor  string            `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, "", fmt.Errorf("parse changes: %w", err)
	}
	return len(out.Changes), out.Cursor, nil
}

// dispatch starts the holder's worker if needed and opens a run of the agent
// in its workspace. LastRun is set at dispatch, whatever happens next, so a
// failing door cannot loop faster than the minimum interval.
func (e *routineEngine) dispatch(ctx context.Context, st *routineState, a capability.AgentSpec) {
	now := e.now()
	e.mu.Lock()
	st.LastRun[a.Name] = now
	delete(st.pending, a.Name)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(st.dispatching, a.Name)
		e.mu.Unlock()
	}()

	w := e.mgr.Ensure(st.Subject)
	deadline := now.Add(routineStartWait)
	for !w.IsReady() {
		if e.now().After(deadline) || ctx.Err() != nil {
			log.Printf("[routines] %.8s…/%s: worker not ready after %s; run skipped", st.Subject, a.Name, routineStartWait)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		w = e.mgr.Ensure(st.Subject)
	}
	title, prompt := runTitleAndPrompt(a, now)
	body, _ := json.Marshal(map[string]string{"workspacePath": a.Path, "title": title, "prompt": prompt})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.RoutineDoor(), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// The door is a route on the worker's own server, behind the same token
	// this proxy presents on every request to it.
	req.Header.Set("X-Privasys-Ingress-Token", w.Ingress)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[routines] %.8s…/%s: the routines door did not answer: %v", st.Subject, a.Name, err)
		return
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusAccepted {
		log.Printf("[routines] %.8s…/%s: run refused: %d %s", st.Subject, a.Name, resp.StatusCode, truncate(reply, 200))
		return
	}
	log.Printf("[routines] %.8s…/%s: run dispatched: %q", st.Subject, a.Name, title)
}

// runTitleAndPrompt names the run's session and says why it runs.
func runTitleAndPrompt(a capability.AgentSpec, now time.Time) (title, prompt string) {
	title = fmt.Sprintf("%s %s", a.Name, now.UTC().Format("2006-01-02 15:04"))
	prompt = a.Prompt
	if prompt == "" {
		prompt = "Run your routine now, following your agent definition and skills in this workspace."
	}
	switch {
	case a.Trigger.On != "":
		prompt += "\n\n(Started unattended because new activity arrived on " + a.Trigger.On + ".)"
	case a.Trigger.Every != "":
		prompt += "\n\n(Started unattended on the schedule of every " + a.Trigger.Every + ".)"
	}
	return title, prompt
}
