// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Routines: unattended runs of the holder's agents (plan §3.3, decided
// 2026-09-14). Unattended runs come from THIS proxy, not from dsh, which has
// no scheduler and nothing that wakes a cold session.
//
// Each agent folder in the holder's Drive carries an agent.yaml with a
// trigger (capability/agents.go). This engine owns the clock and the events:
//
//   - `every: 2h`        a timer.
//   - `on: mail.changes` ONE held long poll per holder and agent on the mail
//                        connector's `changes` tool, over the same attested
//                        tool leg the agent uses, as the holder (the connector
//                        parks it on IMAP IDLE for a minute at a time, so a
//                        quiet mailbox costs one held request a minute and a
//                        message wakes the routine within seconds). The
//                        connector never calls in; the harness has no attested
//                        door and the tool stays the callee.
//
// A run is dispatched through the worker's routines door (workers.go,
// app/privasys-routines.mjs): a POST that dsh's webhook runtime turns into a
// new session in the agent's workspace, so the holder reads it where they
// read everything else. Bursts are debounced and runs are spaced by the
// agent's minimum interval, which is also the one-run-at-a-time guard: the
// door answers 202 at dispatch and never reports completion.
//
// Authority is durable without a browser: grants live on the manager, the
// worker is started from the subject string alone, and the agents a holder
// declared are remembered on the encrypted volume so the poll is held while
// they are away.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

const (
	routineTick      = 30 * time.Second
	routinePollWait  = 60 // seconds the connector parks one changes call
	routinesFile     = "routines.json"
	routineDoorToken = "X-Privasys-Routine-Token"
	// routineStartWait bounds how long a dispatch waits for a cold worker.
	routineStartWait = 3 * time.Minute
	// mailChangesTrigger is the one event source this release understands.
	mailChangesTrigger = "mail.changes"
)

// routineState is what the engine remembers per holder, persisted beside
// their cache on the volume so a restart, or a holder who never opens the
// assistant, still gets their runs.
type routineState struct {
	Subject string                 `json:"subject"`
	Agents  []capability.AgentSpec `json:"agents"`
	// Cursors is the connector's change cursor per agent; LastRun the last
	// dispatch per agent.
	Cursors map[string]string    `json:"cursors"`
	LastRun map[string]time.Time `json:"last_run"`

	pending map[string]time.Time          // agent -> first event of the current burst
	pollers map[string]context.CancelFunc // agent -> the held poll
}

type routineEngine struct {
	mgr      *WorkerManager
	client   *http.Client
	mailHost string
	usersDir string
	now      func() time.Time

	mu       sync.Mutex
	subjects map[string]*routineState
}

func newRoutineEngine(mgr *WorkerManager, client *http.Client, mailHost, usersDir string) *routineEngine {
	return &routineEngine{mgr: mgr, client: client, mailHost: mailHost, usersDir: usersDir,
		now: time.Now, subjects: map[string]*routineState{}}
}

// Load re-arms every holder remembered on the volume.
func (e *routineEngine) Load() {
	entries, err := os.ReadDir(e.usersDir)
	if err != nil {
		return
	}
	n := 0
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(e.usersDir, d.Name(), routinesFile))
		if err != nil {
			continue
		}
		var st routineState
		if json.Unmarshal(raw, &st) != nil || st.Subject == "" {
			continue
		}
		e.mu.Lock()
		e.subjects[st.Subject] = e.fresh(&st)
		e.mu.Unlock()
		n++
	}
	if n > 0 {
		log.Printf("[routines] %d holder(s) with agents remembered from the volume", n)
	}
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
// dispatches whatever is due.
func (e *routineEngine) tick(ctx context.Context) {
	e.mgr.Each(func(w *Worker) {
		if w.Subject == systemSubject || w.Syncer() == nil {
			return
		}
		e.update(w.Subject, w.Syncer().Agents())
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
	changed := !sameAgents(st.Agents, agents)
	st.Agents = agents
	e.mu.Unlock()
	if changed {
		e.persist(st)
	}
}

func sameAgents(a, b []capability.AgentSpec) bool {
	if len(a) != len(b) {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// reconcile arms one held poll per mail-triggered agent, cancels polls of
// agents no longer scheduled, and dispatches due runs.
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
		if a, ok := scheduled[name]; !ok || a.Trigger.On != mailChangesTrigger {
			cancel()
			delete(st.pollers, name)
		}
	}
	var due []capability.AgentSpec
	for name, a := range scheduled {
		if a.Trigger.On == mailChangesTrigger && st.pollers[name] == nil && e.mailHost != "" {
			pctx, cancel := context.WithCancel(ctx)
			st.pollers[name] = cancel
			go e.poll(pctx, st, a)
		}
		if runDue(a, now, st.LastRun[name], st.pending[name]) {
			due = append(due, a)
		}
	}
	e.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].Name < due[j].Name })
	for _, a := range due {
		e.dispatch(ctx, st, a)
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

// poll holds the connector's changes call for one holder and agent, as the
// holder, over the attested tool leg; arrivals open or extend a burst.
func (e *routineEngine) poll(ctx context.Context, st *routineState, a capability.AgentSpec) {
	log.Printf("[routines] %.8s…/%s: holding the mailbox change feed", st.Subject, a.Name)
	for ctx.Err() == nil {
		e.mu.Lock()
		cursor := st.Cursors[a.Name]
		e.mu.Unlock()
		changes, next, err := e.changes(ctx, st.Subject, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[routines] %.8s…/%s: change feed: %v (retrying in a minute)", st.Subject, a.Name, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			continue
		}
		e.mu.Lock()
		if next != "" {
			st.Cursors[a.Name] = next
		}
		if changes > 0 && st.pending[a.Name].IsZero() {
			st.pending[a.Name] = e.now()
		}
		e.mu.Unlock()
		if next != "" && next != cursor {
			e.persist(st)
		}
	}
}

// changes makes one held call to the connector's changes tool as the holder.
func (e *routineEngine) changes(ctx context.Context, sub, cursor string) (int, string, error) {
	args, _ := json.Marshal(map[string]any{"since": cursor, "wait_seconds": routinePollWait})
	cctx, cancel := context.WithTimeout(ctx, (routinePollWait+30)*time.Second)
	defer cancel()
	carrier, _ := http.NewRequestWithContext(cctx, http.MethodPost, "/", nil)
	raw, status, _, err := callTool(carrier, e.client, e.mailHost, "changes", args, "", sub)
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
	e.persist(st)

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
	req.Header.Set(routineDoorToken, w.Ingress)
	// dsh's ingress-token guard (overlay 2b) fronts EVERY listener of the
	// worker, the isolated one included: without this header the door
	// answers 401 before the plugin sees the request (seen 2026-09-14).
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

// persist writes a holder's state beside their cache on the volume.
func (e *routineEngine) persist(st *routineState) {
	e.mu.Lock()
	raw, err := json.MarshalIndent(st, "", "  ")
	e.mu.Unlock()
	if err != nil {
		return
	}
	dir := filepath.Join(e.usersDir, workerKey(st.Subject))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, routinesFile), raw, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		log.Printf("[routines] %.8s…: remembering agents: %v", st.Subject, err)
	}
}
