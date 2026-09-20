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
//   - `at: "0 17 * * FRI"` a cron schedule, read in UTC (the harness knows
//                          no holder's timezone); a run is due when the next
//                          schedule time after the last run, or after this
//                          process started, has passed.
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
//
// One refusal is not a failure to retry: a tool that answers 403 with
// `"needs_holder": true` has nothing for this holder until they act on their
// device (an approval, a setup the service asks of them). Polling a service
// that says so is asking a locked door every minute. Instead the engine puts
// the question to the holder ONCE, through the runtime, for the resource the
// tool serves (the same ask the access server's request_access makes with
// ask_again), and then holds the feed until the runtime's event stream says
// the holder approved, or the agent is gone. A denial is logged once and
// keeps the hold: the holder said no, and they reopen it from the chat.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	runs        map[string]dayRuns            // agent -> today's dispatch count
	capSaid     map[string]string             // agent -> the UTC day the cap refusal was logged
}

// dayRuns counts one agent's dispatches on one UTC day.
type dayRuns struct {
	day string
	n   uint64
}

type routineEngine struct {
	mgr       *WorkerManager
	client    *http.Client
	toolHosts map[string]string
	now       func() time.Time
	// started is when this process's clock began: a schedule's first run is
	// its next time after this, never one the process was down for.
	started time.Time

	// resourceFor names the declared resource a tool serves for a holder
	// (resources.go); askHolder puts that resource to the holder's device,
	// reopening a declined ask (the broker's request with retry). Nil off
	// the platform, where there is nobody to ask.
	resourceFor func(sub, tool string) string
	askHolder   func(sub, resource string) error
	// feed makes one held change-feed call (changes); tests replace it.
	feed func(ctx context.Context, sub, tool, call, cursor string) (int, string, error)
	// runsPerDay is the holder's cap on unattended runs per agent and UTC
	// day (policy `spend.routines.runs_per_day`); nil or 0 is unlimited.
	runsPerDay func(sub string) uint64
	// sessionCwd names the working directory of one of the holder's dsh
	// sessions, from its log, so a model-leg refusal can be laid at a run's
	// door: a run's session works in its agent's directory.
	sessionCwd func(sub, session string) string
	// pauser gives the holder's agent writer (capability.Syncer.PauseAgent);
	// nil when their worker is gone.
	pauser func(sub string) func(name, why string, at time.Time) error

	mu       sync.Mutex
	subjects map[string]*routineState
	// holds are the change feeds waiting for the holder, per subject
	// (holderHold); the runtime's events release them (Event).
	holds map[string][]*holderHold
}

func newRoutineEngine(mgr *WorkerManager, client *http.Client, toolHosts map[string]string) *routineEngine {
	e := &routineEngine{mgr: mgr, client: client, toolHosts: toolHosts,
		now: time.Now, started: time.Now(), subjects: map[string]*routineState{}, holds: map[string][]*holderHold{}}
	e.feed = e.changes
	return e
}

// routineEng is the process's engine, set by main when workers are on: what
// the runtime's event stream (events.go) reaches. Nil in the single-user
// layout, where there are no routines.
var routineEng *routineEngine

// errNeedsHolder is a change-feed refusal that says the tool has nothing for
// this holder until they act on their device: not a failure to back off from.
var errNeedsHolder = errors.New("the tool needs the holder")

// holderHold is one change feed waiting for the holder's approval.
type holderHold struct {
	agent    string
	resource string // "" when no declared resource serves the tool
	released chan struct{}
	done     bool // released once; a channel closes once
	denied   bool // the denial was logged; the hold stays
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
	st.runs = map[string]dayRuns{}
	st.capSaid = map[string]string{}
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
		if !st.dispatching[name] && runDue(a, now, st.LastRun[name], st.pending[name], e.started) {
			if e.pastDailyCap(st, name, now) {
				continue
			}
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

// runLiveWindow is how long after a dispatch a model-leg refusal that names
// no agent's session is still laid at that run's door: the door never
// reports completion, and a run's first model call comes within this.
const runLiveWindow = 10 * time.Minute

// pastDailyCap counts a dispatch against the holder's runs-per-day cap and
// reports whether the cap refuses it, saying so once per agent and UTC day.
// The caller holds e.mu.
func (e *routineEngine) pastDailyCap(st *routineState, name string, now time.Time) bool {
	day := now.UTC().Format("2006-01-02")
	r := st.runs[name]
	if r.day != day {
		r = dayRuns{day: day}
	}
	var limit uint64
	if e.runsPerDay != nil {
		limit = e.runsPerDay(st.Subject)
	}
	if limit > 0 && r.n >= limit {
		if st.capSaid[name] != day {
			st.capSaid[name] = day
			log.Printf("[routines] %.8s…/%s: %d run(s) today is the holder's cap (spend.routines.runs_per_day); no more until tomorrow, UTC", st.Subject, name, r.n)
		}
		return true
	}
	r.n++
	st.runs[name] = r
	return false
}

// modelPaymentRefused is told of a 402 on the model leg (main.go forward),
// with the acting subject and the dsh session the call was for; set by main
// to the engine's paymentRefused when workers are on.
var modelPaymentRefused func(sub, session string)

// paymentRefused pauses every agent of the holder when the model service
// refuses one of their runs for payment: an unattended agent must not go
// on spending against an account that says no, and nobody is watching to
// stop it. The run is known by its session's working directory (an agent's
// own); a session whose log names none yet is taken for a run when one was
// dispatched within runLiveWindow. A holder's own conversation refused with
// no run about pauses nothing: they are there to read the answer.
func (e *routineEngine) paymentRefused(sub, session string) {
	e.mu.Lock()
	st := e.subjects[sub]
	if st == nil {
		e.mu.Unlock()
		return
	}
	agents := append([]capability.AgentSpec(nil), st.Agents...)
	lastRun := make(map[string]time.Time, len(st.LastRun))
	for k, v := range st.LastRun {
		lastRun[k] = v
	}
	e.mu.Unlock()

	cwd := ""
	if e.sessionCwd != nil && session != "" {
		cwd = e.sessionCwd(sub, session)
	}
	culprit := ""
	for _, a := range agents {
		if cwd != "" && a.Path != "" && (cwd == a.Path || strings.HasPrefix(cwd, a.Path+"/") || strings.HasPrefix(cwd, a.Path+"\\")) {
			culprit = a.Name
		}
	}
	if culprit == "" {
		now := e.now()
		for name, t := range lastRun {
			if now.Sub(t) <= runLiveWindow {
				culprit = name
			}
		}
	}
	if culprit == "" {
		return
	}
	e.pauseAll(st, fmt.Sprintf("the model service refused a run of %q for payment (HTTP 402)", culprit))
}

// pauseAll pauses every agent of the holder that is not paused yet, in
// memory at once (a second refusal in flight must not write twice) and in
// their definitions.
func (e *routineEngine) pauseAll(st *routineState, why string) {
	now := e.now()
	e.mu.Lock()
	var names []string
	for i := range st.Agents {
		if !st.Agents[i].Paused {
			st.Agents[i].Paused = true
			names = append(names, st.Agents[i].Name)
		}
	}
	e.mu.Unlock()
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	log.Printf("[routines] %.8s…: %s; pausing every agent of the holder (%s)", st.Subject, why, strings.Join(names, ", "))
	var pause func(name, why string, at time.Time) error
	if e.pauser != nil {
		pause = e.pauser(st.Subject)
	}
	for _, name := range names {
		if pause == nil {
			log.Printf("[routines] %.8s…/%s: paused in memory only; the holder's worker is gone, so the definition could not be written", st.Subject, name)
			continue
		}
		if err := pause(name, why+"; ask the chat to unpause this agent once the account is funded", now); err != nil {
			log.Printf("[routines] %.8s…/%s: pausing: %v", st.Subject, name, err)
		}
	}
}

// runDue decides whether an agent runs now: a timer that elapsed, a schedule
// whose next time after the last run (or after this engine started, for a
// fresh process) has passed, or a burst of events that settled for the
// debounce, and in every case not before the minimum interval since the last
// run, which is also the one-at-a-time guard.
func runDue(a capability.AgentSpec, now, lastRun, pendingSince, started time.Time) bool {
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
	if sched := a.Schedule(); sched != nil {
		// In UTC: the harness knows no holder's timezone, and the README
		// says so. A schedule time that passed while the process was down
		// is not caught up on; the next one after the start is.
		base := lastRun
		if base.IsZero() {
			base = started
		}
		if next := sched.Next(base.UTC()); !next.After(now.UTC()) {
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
		changes, next, err := e.feed(ctx, st.Subject, tool, call, cursor)
		if errors.Is(err, errNeedsHolder) {
			// Not a failure: the tool is waiting for the holder, so this
			// engine waits with it, without a second call until they act.
			if !e.holdForHolder(ctx, st, a, tool) {
				return
			}
			failures = 0
			continue
		}
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
	if holderNeeded(status, raw) {
		return 0, "", fmt.Errorf("%w: %s", errNeedsHolder, truncate(raw, 200))
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

// holderNeeded reads a tool's refusal for the one thing it may say beyond
// "no": that the holder has to act on their device before the feed has
// anything for them (HTTP 403 with `"needs_holder": true`). Any tool may
// say it; what the holder has to do is the tool's business.
func holderNeeded(status int, raw []byte) bool {
	if status != http.StatusForbidden {
		return false
	}
	var body struct {
		NeedsHolder bool `json:"needs_holder"`
	}
	return json.Unmarshal(raw, &body) == nil && body.NeedsHolder
}

// holdForHolder asks the holder's device once for the resource the tool
// serves, then holds the feed until the runtime reports an approval for it
// (Event) or ctx ends. It reports whether the feed may go on.
func (e *routineEngine) holdForHolder(ctx context.Context, st *routineState, a capability.AgentSpec, tool string) bool {
	resource := ""
	if e.resourceFor != nil {
		resource = e.resourceFor(st.Subject, tool)
	}
	h := &holderHold{agent: a.Name, resource: resource, released: make(chan struct{})}
	e.mu.Lock()
	e.holds[st.Subject] = append(e.holds[st.Subject], h)
	e.mu.Unlock()
	defer e.dropHold(st.Subject, h)

	switch {
	case resource == "":
		log.Printf("[routines] %.8s…/%s: the %s change feed needs the holder, and no declared resource serves %s, so there is nothing to ask for; waiting for any approval of theirs",
			st.Subject, a.Name, a.Trigger.On, tool)
	case e.askHolder == nil:
		log.Printf("[routines] %.8s…/%s: the %s change feed needs the holder for %s; nothing here can ask their device, waiting for the approval",
			st.Subject, a.Name, a.Trigger.On, resource)
	default:
		if err := e.askHolder(st.Subject, resource); err != nil {
			log.Printf("[routines] %.8s…/%s: asking the holder's device for %s failed (%v); waiting for the approval", st.Subject, a.Name, resource, err)
		} else {
			log.Printf("[routines] %.8s…/%s: asked the holder's device for %s; waiting for the approval", st.Subject, a.Name, resource)
		}
	}
	select {
	case <-h.released:
		log.Printf("[routines] %.8s…/%s: the holder approved; the %s change feed resumes", st.Subject, a.Name, a.Trigger.On)
		return true
	case <-ctx.Done():
		return false
	}
}

// dropHold forgets a hold whose poll ended, however it ended.
func (e *routineEngine) dropHold(sub string, h *holderHold) {
	e.mu.Lock()
	defer e.mu.Unlock()
	kept := e.holds[sub][:0]
	for _, x := range e.holds[sub] {
		if x != h {
			kept = append(kept, x)
		}
	}
	if len(kept) == 0 {
		delete(e.holds, sub)
	} else {
		e.holds[sub] = kept
	}
}

// Event is what the runtime's stream means for the feeds waiting on the
// holder: an approval of the resource a hold names (or any approval, for a
// hold that could name none) releases it; a denial is said once and keeps
// it, since the holder reopens a declined ask from the chat. Every other
// event is nobody's business here.
func (e *routineEngine) Event(ev capability.Event) {
	if ev.Subject == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, h := range e.holds[ev.Subject] {
		matches := h.resource == "" || h.resource == ev.Resource
		switch {
		case ev.Type == "capability.approved" && matches && !h.done:
			h.done = true
			close(h.released)
		case ev.Type == "capability.denied" && h.resource == ev.Resource && !h.denied:
			h.denied = true
			log.Printf("[routines] %.8s…/%s: the holder declined %s; the change feed keeps waiting (they can reopen it from the chat)", ev.Subject, h.agent, ev.Resource)
		}
	}
}

// waiting reports whether the agent's feed is held for the holder (tests).
func (e *routineEngine) waiting(sub, agent string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, h := range e.holds[sub] {
		if h.agent == agent && !h.done {
			return true
		}
	}
	return false
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
	case a.Trigger.At != "":
		prompt += "\n\n(Started unattended on the schedule " + a.Trigger.At + ".)"
	}
	return title, prompt
}
