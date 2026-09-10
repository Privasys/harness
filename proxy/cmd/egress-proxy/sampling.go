// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Sampling pins and faithful replay on the /model leg.
//
// Confidential AI's headline claim is that the same model, weights, seed and
// prompt on the same hardware reproduce a reply byte for byte. The chat front
// demonstrated it with a per-reply metadata dialog and a "replay with the
// recorded pins" action. In the harness the model request is assembled by
// dsh, which exposes only model, effort, temperature and max_tokens — no
// seed, no top_p/top_k, and no way to re-issue a turn with a recorded wall
// clock. This file gives the measured proxy that role, per D2: the trust
// artefact (what was sent, what came back) is handled on the attested leg by
// Go, never left to Node to request or drop.
//
//	GET    /privasys/sampling?session=<id>   the acting user's pins for one
//	                                         dsh session, and any armed replay
//	PUT    /privasys/sampling?session=<id>   set pins and/or arm a replay
//	DELETE /privasys/sampling?session=<id>   clear both
//
// Pins ride every model call of that session (dsh names the session on the
// wire with x-deepseek-harness-session-id). A replay is a list of recorded
// steps; each incoming call that has the recorded SHAPE (message and tool
// counts) consumes the next step: its seed and sampling are pinned, its
// dynamic context is handed back to Confidential AI verbatim, and the
// trailing time-context message dsh injects is normalised to the recorded
// text — a fresh clock is a different prompt and produces different tokens.
// The proxy then compares the canonical prompt with the recorded digest and
// says so in the reproducibility block, so the UI can claim "prompt
// identical" only when it is.
//
// A turn that used tools is only reproducible if the tools answer the same:
// a weather page fetched again is a different prompt on the next model call.
// So a plan also carries the turn's recorded TOOL RESULTS, in order, and the
// MCP shim (mcpshim.go) serves the next recorded result to a tool call that
// matches it (same server, same tool, same canonical arguments) instead of
// dialling the tool. A call that matches nothing is made live, as usual. A
// replay therefore reaches no tool app and incurs no tool fee.
//
// Process-local: a restart drops the pins, which is the right reading for a
// harness that restarts on every deploy. The durable record is the
// reproducibility block itself, which the dsh translator folds into the
// session log (apply-overlay.mjs §2h).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxModelBodyBytes bounds a chat completion the proxy is willing to parse.
// Requests with images run to megabytes; anything past this passes through
// untouched (no pins, no annotation) rather than failing the turn.
const maxModelBodyBytes = 32 << 20

// maxSamplingBytes bounds a PUT body: pins and a few dozen replay steps.
const maxSamplingBytes = 256 << 10

// maxSamplingSessions bounds the book; the oldest entries are evicted.
const maxSamplingSessions = 4096

// timeContextPrefix opens the message dsh-time-context appends at the tail
// of every step (packages/context/time-context, renderText).
const timeContextPrefix = "Time sampled while preparing turn"

// sessionHeader is how dsh names the session on a model request.
const sessionHeader = "x-deepseek-harness-session-id"

// samplingPins are the OpenAI-compatible sampling fields a user can pin for
// a session. Absent fields leave dsh's request untouched.
type samplingPins struct {
	Seed        *int64   `json:"seed,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int64   `json:"top_k,omitempty"`
	MaxTokens   *int64   `json:"max_tokens,omitempty"`
}

func (p *samplingPins) empty() bool {
	return p == nil || (p.Seed == nil && p.Temperature == nil && p.TopP == nil && p.TopK == nil && p.MaxTokens == nil)
}

func (p *samplingPins) validate() error {
	if p == nil {
		return nil
	}
	if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
		return fmt.Errorf("temperature must be within [0, 2]")
	}
	if p.TopP != nil && (*p.TopP <= 0 || *p.TopP > 1) {
		return fmt.Errorf("top_p must be within (0, 1]")
	}
	if p.TopK != nil && *p.TopK < -1 {
		return fmt.Errorf("top_k must be -1 (off) or non-negative")
	}
	if p.MaxTokens != nil && (*p.MaxTokens < 1 || *p.MaxTokens > 1<<20) {
		return fmt.Errorf("max_tokens must be within [1, 1048576]")
	}
	return nil
}

// apply writes the pinned fields over the wire body.
func (p *samplingPins) apply(body map[string]any) {
	if p == nil {
		return
	}
	if p.Seed != nil {
		body["seed"] = *p.Seed
	}
	if p.Temperature != nil {
		body["temperature"] = *p.Temperature
	}
	if p.TopP != nil {
		body["top_p"] = *p.TopP
	}
	if p.TopK != nil {
		body["top_k"] = *p.TopK
	}
	if p.MaxTokens != nil {
		body["max_tokens"] = *p.MaxTokens
	}
}

// replayOf names the turn a replay reproduces.
type replayOf struct {
	Session   string `json:"session"`
	MessageID string `json:"message_id,omitempty"`
	Turn      int    `json:"turn,omitempty"`
}

// replayStep is one recorded model call of the turn being replayed: the
// pins Confidential AI echoed in its reproducibility block, plus what the
// proxy annotated about the request.
type replayStep struct {
	samplingPins
	DynamicContext string `json:"dynamic_context,omitempty"`
	TimeContext    string `json:"time_context,omitempty"`
	PromptDigest   string `json:"prompt_digest,omitempty"`
	MessagesCount  int    `json:"messages_count"`
	ToolsCount     int    `json:"tools_count"`
}

// replayTool is one recorded tool call of the turn being replayed: which
// tool, the canonical digest of the arguments the model gave it, and the
// result it produced. The shim hands the result back to an identical call.
type replayTool struct {
	Server     string          `json:"server"`
	Name       string          `json:"name"`
	ArgsDigest string          `json:"args_digest"`
	Content    json.RawMessage `json:"content"`
	IsError    bool            `json:"is_error,omitempty"`
}

// replayPlan is an armed replay: the steps still to consume, in order.
type replayPlan struct {
	Of    replayOf     `json:"of"`
	Steps []replayStep `json:"steps"`
	// Consumed counts the steps already applied, so the panel can say
	// "step 2 of 3" while the plan is in flight.
	Consumed int `json:"consumed"`
	// Tools are the recorded tool results still to serve, in order;
	// ToolsConsumed counts those already served.
	Tools         []replayTool `json:"tools,omitempty"`
	ToolsConsumed int          `json:"tools_consumed"`
	// done is when the last step was consumed. An exhausted plan stays
	// for replayGrace so a model call the record did not have (the model
	// took a longer path this time) is still annotated as part of the
	// replay, and named as beyond it, instead of looking like an ordinary
	// unpinned call. Zero while steps remain.
	done time.Time
}

// replayGrace is how long an exhausted plan keeps annotating the session's
// further model calls.
const replayGrace = 10 * time.Minute

// Skip reasons a replayed call reports.
const (
	replaySkipShape  = "shape"  // the call's shape differs from the next recorded step
	replaySkipBeyond = "beyond" // the recorded turn had no more steps
)

// sessionSampling is the book entry for one (subject, dsh session).
type sessionSampling struct {
	Pins      *samplingPins `json:"pins,omitempty"`
	Replay    *replayPlan   `json:"replay,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// samplingBook keeps per-subject, per-session pins. Subject-keyed, so a
// user can only ever pin their own sessions: the session id is free text
// under that subject and names nobody else's process.
type samplingBook struct {
	mu   sync.Mutex
	book map[string]*sessionSampling
}

func newSamplingBook() *samplingBook {
	return &samplingBook{book: map[string]*sessionSampling{}}
}

// sampling is the process-wide book: the ingress API writes it, the model
// leg reads it.
var sampling = newSamplingBook()

func samplingKey(sub, session string) string { return sub + "\x00" + session }

// get returns a copy of the entry, or nil.
func (b *samplingBook) get(sub, session string) *sessionSampling {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.book[samplingKey(sub, session)]
	if e == nil {
		return nil
	}
	return e.clone()
}

// peekReplayTimeContext returns the time-context text the next replay
// step of (sub, session) will impose, without consuming the step. dsh's
// own time-context plugin asks for it before it samples the clock, so the
// SESSION RECORD (the trajectory a user reads) carries the recorded time
// rather than a fresh one; the proxy still normalises the model leg, so a
// worker that never asked gets the same prompt bytes either way.
func (b *samplingBook) peekReplayTimeContext(sub, session string) (text string, pinned bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.book[samplingKey(sub, session)]
	if e == nil || e.Replay == nil || e.Replay.Consumed >= len(e.Replay.Steps) {
		return "", false
	}
	return e.Replay.Steps[e.Replay.Consumed].TimeContext, true
}

func (e *sessionSampling) clone() *sessionSampling {
	c := *e
	if e.Pins != nil {
		p := *e.Pins
		c.Pins = &p
	}
	if e.Replay != nil {
		r := *e.Replay
		r.Steps = append([]replayStep(nil), e.Replay.Steps...)
		r.Tools = append([]replayTool(nil), e.Replay.Tools...)
		c.Replay = &r
	}
	return &c
}

// merge updates the entry: a present `pins` replaces the pins (null clears),
// a present `replay` replaces the plan (null disarms); an absent key keeps
// the current value.
func (b *samplingBook) merge(sub, session string, pins *samplingPins, pinsSet bool, replay *replayPlan, replaySet bool) *sessionSampling {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := samplingKey(sub, session)
	e := b.book[k]
	if e == nil {
		e = &sessionSampling{}
		b.book[k] = e
		b.evictLocked()
	}
	if pinsSet {
		if pins.empty() {
			e.Pins = nil
		} else {
			e.Pins = pins
		}
	}
	if replaySet {
		if replay == nil || len(replay.Steps) == 0 {
			e.Replay = nil
		} else {
			// The book owns its copy: consuming steps must not reach back
			// into the caller's slice.
			r := *replay
			r.Steps = append([]replayStep(nil), replay.Steps...)
			r.Tools = append([]replayTool(nil), replay.Tools...)
			e.Replay = &r
		}
	}
	e.UpdatedAt = time.Now().UTC()
	if e.Pins == nil && e.Replay == nil {
		delete(b.book, k)
		return e.clone()
	}
	return e.clone()
}

func (b *samplingBook) del(sub, session string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.book, samplingKey(sub, session))
}

// evictLocked drops the oldest entries past the cap.
func (b *samplingBook) evictLocked() {
	if len(b.book) <= maxSamplingSessions {
		return
	}
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(b.book))
	for k, e := range b.book {
		all = append(all, kv{k, e.UpdatedAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	for _, x := range all[:len(all)-maxSamplingSessions] {
		delete(b.book, x.k)
	}
}

// takeReplayStep consumes the next armed step when the incoming call has
// the recorded shape. A call of another shape (dsh's session-title request,
// say) leaves the plan untouched and is reported as skipped with the
// reason; so is a call made after the recorded turn's last step, for a
// while, since a model that took a longer path this time still belongs to
// the replay and its fresh seed must be named as such.
func (b *samplingBook) takeReplayStep(sub, session string, messages, tools int) (step *replayStep, of *replayOf, index int, skipped string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	k := samplingKey(sub, session)
	e := b.book[k]
	if e == nil || e.Replay == nil {
		return nil, nil, 0, ""
	}
	if len(e.Replay.Steps) == 0 {
		o := e.Replay.Of
		if time.Since(e.Replay.done) > replayGrace {
			e.Replay = nil
			if e.Pins == nil {
				delete(b.book, k)
			}
			return nil, nil, 0, ""
		}
		return nil, &o, e.Replay.Consumed, replaySkipBeyond
	}
	next := e.Replay.Steps[0]
	if next.MessagesCount != messages || next.ToolsCount != tools {
		o := e.Replay.Of
		return nil, &o, e.Replay.Consumed, replaySkipShape
	}
	e.Replay.Steps = e.Replay.Steps[1:]
	e.Replay.Consumed++
	o := e.Replay.Of
	idx := e.Replay.Consumed
	if len(e.Replay.Steps) == 0 {
		// The last model call of the turn. Any recorded tool result still
		// unserved belongs to a call the model did not make this time,
		// which the prompt verdict already says: drop them so nothing else
		// can consume them, and keep the plan itself for the grace period.
		e.Replay.Tools = nil
		e.Replay.done = time.Now()
	}
	return &next, &o, idx, ""
}

// takeReplayTool serves the next recorded tool result of any replay the
// subject has armed, when the call matches it exactly: same tool server and
// tool, same canonical arguments. Tool calls carry no session id on the
// wire, so the match is what ties a call to its plan; a call that matches
// nothing is made live. Returns nil when nothing matched.
func (b *samplingBook) takeReplayTool(sub, server, name, argsDigest string) *replayTool {
	if sub == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	prefix := sub + "\x00"
	for k, e := range b.book {
		if !strings.HasPrefix(k, prefix) || e.Replay == nil || len(e.Replay.Tools) == 0 {
			continue
		}
		next := e.Replay.Tools[0]
		if next.Server != server || next.Name != name || next.ArgsDigest != argsDigest {
			continue
		}
		e.Replay.Tools = e.Replay.Tools[1:]
		e.Replay.ToolsConsumed++
		return &next
	}
	return nil
}

// argsDigest is the SHA-256 of a tool call's arguments in canonical JSON:
// object keys sorted, no HTML escaping, no insignificant whitespace. The UI
// computes the same digest from the recorded call, so a replayed call and
// its record meet on content, whatever key order the model emitted.
func argsDigest(args json.RawMessage) string {
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		v = string(args)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return hex.EncodeToString(sum[:])
}

// modelCall is what the proxy knows about one chat completion it forwarded:
// the annotation it adds to the reproducibility trailer.
type modelCall struct {
	RequestID      string
	Session        string
	At             time.Time
	PromptDigest   string
	TimeContext    string
	MessagesCount  int
	ToolsCount     int
	Pins           *samplingPins
	Replay         *replayOf
	ReplayStep     int
	ReplaySkipped  string // "" when a step was applied; else replaySkipShape or replaySkipBeyond
	PromptMatch    *bool
	ExpectedDigest string
	// ExpectedDynamicContext is the clock the replay step handed back to
	// Confidential AI; DynamicContextMatch says whether the trailer shows
	// that same clock was stamped (repro.go), or is nil off replay.
	ExpectedDynamicContext string
	DynamicContextMatch    *bool
	// ToolsReplayed counts the recorded tool results served so far in
	// this replay, at the time of this call.
	ToolsReplayed int
}

// annotation is the `harness` object merged into the reproducibility block.
func (c *modelCall) annotation() map[string]any {
	m := map[string]any{
		"request_id":     c.RequestID,
		"at":             c.At.Format(time.RFC3339Nano),
		"prompt_digest":  c.PromptDigest,
		"messages_count": c.MessagesCount,
		"tools_count":    c.ToolsCount,
	}
	if c.Session != "" {
		m["session"] = c.Session
	}
	if c.TimeContext != "" {
		m["time_context"] = c.TimeContext
	}
	if !c.Pins.empty() {
		m["pins"] = c.Pins
	}
	if c.Replay != nil {
		r := map[string]any{"of": c.Replay, "step": c.ReplayStep}
		if c.ReplaySkipped != "" {
			r["skipped"] = c.ReplaySkipped
		} else {
			r["prompt_match"] = c.PromptMatch != nil && *c.PromptMatch
			r["expected_prompt_digest"] = c.ExpectedDigest
			if c.DynamicContextMatch != nil {
				r["dynamic_context_match"] = *c.DynamicContextMatch
			}
		}
		if c.ToolsReplayed > 0 {
			r["tools_replayed"] = c.ToolsReplayed
		}
		m["replay"] = r
	}
	return m
}

type modelCallKey struct{}

// modelCallOf returns the call record prepareModelCall attached, or nil.
func modelCallOf(r *http.Request) *modelCall {
	c, _ := r.Context().Value(modelCallKey{}).(*modelCall)
	return c
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// prepareModelCall reads a chat completion body, applies the session's pins
// and the next armed replay step, and re-attaches the rewritten body. It
// returns the request to forward (the same one, with the call record in its
// context). Anything that is not a JSON chat completion passes through as
// is. Errors are confined to the request: a body the proxy cannot parse is
// forwarded untouched, never refused.
func prepareModelCall(r *http.Request, book *samplingBook, sub string) *http.Request {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		return r
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxModelBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		return r
	}
	if len(raw) > maxModelBodyBytes {
		log.Printf("[egress-proxy sampling] body over %d bytes; forwarded without pins", maxModelBodyBytes)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		return r
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		return r
	}

	call := &modelCall{
		RequestID: newRequestID(),
		Session:   strings.TrimSpace(r.Header.Get(sessionHeader)),
		At:        time.Now().UTC(),
	}
	call.MessagesCount, call.ToolsCount = requestShape(body)

	entry := book.get(sub, call.Session)
	if entry != nil && entry.Replay != nil {
		call.ToolsReplayed = entry.Replay.ToolsConsumed
		step, of, idx, skipped := book.takeReplayStep(sub, call.Session, call.MessagesCount, call.ToolsCount)
		call.Replay = of
		call.ReplayStep = idx
		call.ReplaySkipped = skipped
		if step != nil {
			normaliseTimeContext(body, step.TimeContext)
			step.samplingPins.apply(body)
			if step.DynamicContext != "" {
				r.Header.Set("X-Privasys-Dynamic-Context", step.DynamicContext)
				call.ExpectedDynamicContext = step.DynamicContext
			}
			call.ExpectedDigest = step.PromptDigest
			p := step.samplingPins
			call.Pins = &p
		}
	}
	// Session pins apply under a replay step too: a user who pinned the
	// session and then replays gets the recorded values, which the step
	// carries in full, so the order below only matters when a step is
	// absent.
	if call.Pins == nil && entry != nil && !entry.Pins.empty() {
		entry.Pins.apply(body)
		call.Pins = entry.Pins
	}
	call.PromptDigest = promptDigest(body)
	call.TimeContext = trailingTimeContext(body)
	if call.ExpectedDigest != "" {
		match := call.ExpectedDigest == call.PromptDigest
		call.PromptMatch = &match
	}
	// A pinned call (a replay step, or a session whose sampling the user
	// pinned) asks Confidential AI for its strict KV-cache mode: a
	// single-use cache salt, so the whole prompt is prefilled fresh.
	// CAI's default salt is per CALLER, so a replay by the same user
	// would otherwise reuse the original's cached prefix while the
	// original computed it cold, and with batch-invariant kernels still
	// absent that difference alone can move a near-tie (seen 2026-09-10:
	// prompt identical, seed identical, 97% cache hit, reply differs).
	if !call.Pins.empty() {
		r.Header.Set("X-Privasys-Reproducibility", "strict")
	}

	out, err := json.Marshal(body)
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		return r
	}
	r.Body = io.NopCloser(bytes.NewReader(out))
	r.ContentLength = int64(len(out))
	r.Header.Set("Content-Length", fmt.Sprint(len(out)))
	if call.Replay != nil || !call.Pins.empty() {
		match := "n/a"
		if call.PromptMatch != nil {
			match = fmt.Sprint(*call.PromptMatch)
		}
		log.Printf("[egress-proxy sampling] request %s session=%.8s pins=%v replay=%v skipped=%q prompt_match=%s digest=%.12s dynctx=%s",
			call.RequestID, call.Session, !call.Pins.empty(), call.Replay != nil, call.ReplaySkipped, match, call.PromptDigest,
			shortDigest(r.Header.Get("X-Privasys-Dynamic-Context")))
	}
	return r.WithContext(context.WithValue(r.Context(), modelCallKey{}, call))
}

// requestShape counts the messages and tools of a wire request.
func requestShape(body map[string]any) (messages, tools int) {
	if m, ok := body["messages"].([]any); ok {
		messages = len(m)
	}
	if t, ok := body["tools"].([]any); ok {
		tools = len(t)
	}
	return messages, tools
}

// promptDigest is the SHA-256 of the canonical JSON of everything that
// conditions the model except the sampling fields: model, messages, tools
// and the thinking configuration. Go marshals map keys sorted, so two
// requests with the same content fold to the same digest whatever field
// order dsh emitted.
func promptDigest(body map[string]any) string {
	sub := map[string]any{}
	for _, k := range []string{"model", "messages", "tools", "tool_choice", "thinking", "reasoning_effort", "response_format"} {
		if v, ok := body[k]; ok {
			sub[k] = v
		}
	}
	b, err := json.Marshal(sub)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// messageText reads a message's text when it is a plain string or a single
// text part; ok is false for anything else (images, tool results, …).
func messageText(m map[string]any) (string, bool) {
	switch c := m["content"].(type) {
	case string:
		return c, true
	case []any:
		if len(c) == 1 {
			if part, ok := c[0].(map[string]any); ok && part["type"] == "text" {
				if s, ok := part["text"].(string); ok {
					return s, true
				}
			}
		}
	}
	return "", false
}

func setMessageText(m map[string]any, text string) {
	switch c := m["content"].(type) {
	case []any:
		if len(c) == 1 {
			if part, ok := c[0].(map[string]any); ok {
				part["text"] = text
				return
			}
		}
	}
	m["content"] = text
}

// timeContextSpan locates the time-context text inside one user message:
// either the whole message (dsh appends it as its own user message) or a
// trailing segment after a blank line (a serializer that merged adjacent
// user messages). Returns the offset of the segment, or -1.
func timeContextSpan(text string) int {
	if strings.HasPrefix(text, timeContextPrefix) {
		return 0
	}
	if i := strings.LastIndex(text, "\n"+timeContextPrefix); i >= 0 {
		return i + 1
	}
	return -1
}

// trailingTimeContext returns the time-context text of the LAST user
// message, or "" when the request carries none.
func trailingTimeContext(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "user" {
			return ""
		}
		text, ok := messageText(m)
		if !ok {
			return ""
		}
		if at := timeContextSpan(text); at >= 0 {
			return text[at:]
		}
		return ""
	}
	return ""
}

// normaliseTimeContext makes the request's trailing time-context equal to
// the recorded one: replaced in place when present, appended as its own
// user message when the recorded step had one and this request has none,
// and removed when the recorded step had none.
func normaliseTimeContext(body map[string]any, want string) {
	msgs, _ := body["messages"].([]any)
	last := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok || m["role"] != "user" {
			break
		}
		if text, ok := messageText(m); ok && timeContextSpan(text) >= 0 {
			last = i
		}
		break
	}
	switch {
	case last >= 0 && want != "":
		m := msgs[last].(map[string]any)
		text, _ := messageText(m)
		at := timeContextSpan(text)
		setMessageText(m, text[:at]+want)
	case last >= 0 && want == "":
		m := msgs[last].(map[string]any)
		text, _ := messageText(m)
		at := timeContextSpan(text)
		if at == 0 {
			body["messages"] = append(msgs[:last:last], msgs[last+1:]...)
		} else {
			setMessageText(m, strings.TrimRight(text[:at], "\n"))
		}
	case last < 0 && want != "":
		body["messages"] = append(msgs, map[string]any{"role": "user", "content": want})
	}
}

// registerSamplingAPI mounts the per-session sampling endpoints on the
// ingress mux, where X-Privasys-Sub is the relay-asserted subject.
func registerSamplingAPI(mux *http.ServeMux, book *samplingBook) {
	subjectAndSession := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return "", "", false
		}
		session := strings.TrimSpace(r.URL.Query().Get("session"))
		if session == "" || len(session) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session query parameter is required"})
			return "", "", false
		}
		return sub, session, true
	}
	respond := func(w http.ResponseWriter, session string, e *sessionSampling) {
		out := map[string]any{"session": session}
		if e != nil {
			if e.Pins != nil {
				out["pins"] = e.Pins
			}
			// An exhausted plan (kept for the grace period so late calls
			// are still named) is not "armed": the chip must not say so.
			if e.Replay != nil && len(e.Replay.Steps) > 0 {
				out["replay"] = map[string]any{
					"of":              e.Replay.Of,
					"remaining":       len(e.Replay.Steps),
					"consumed":        e.Replay.Consumed,
					"tools_remaining": len(e.Replay.Tools),
					"tools_consumed":  e.Replay.ToolsConsumed,
				}
			}
		}
		writeJSON(w, http.StatusOK, out)
	}
	mux.HandleFunc("GET /privasys/sampling", func(w http.ResponseWriter, r *http.Request) {
		sub, session, ok := subjectAndSession(w, r)
		if !ok {
			return
		}
		respond(w, session, book.get(sub, session))
	})
	mux.HandleFunc("PUT /privasys/sampling", func(w http.ResponseWriter, r *http.Request) {
		sub, session, ok := subjectAndSession(w, r)
		if !ok {
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxSamplingBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unreadable body"})
			return
		}
		// Decoded as a map so an explicit null (clear) is told apart from an
		// absent key (keep): a *json.RawMessage collapses both to nil.
		var req map[string]json.RawMessage
		if err := json.Unmarshal(raw, &req); err != nil || req == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be a JSON object with pins and/or replay"})
			return
		}
		var pins *samplingPins
		pinsRaw, pinsSet := req["pins"]
		if pinsSet && string(pinsRaw) != "null" {
			pins = &samplingPins{}
			if err := json.Unmarshal(pinsRaw, pins); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pins: " + err.Error()})
				return
			}
			if err := pins.validate(); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pins: " + err.Error()})
				return
			}
		}
		var replay *replayPlan
		replayRaw, replaySet := req["replay"]
		if replaySet && string(replayRaw) != "null" {
			replay = &replayPlan{}
			if err := json.Unmarshal(replayRaw, replay); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "replay: " + err.Error()})
				return
			}
			if len(replay.Tools) > 64 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "replay: at most 64 recorded tool results"})
				return
			}
			for i, tool := range replay.Tools {
				if tool.Server == "" || tool.Name == "" || len(tool.ArgsDigest) != 64 || len(tool.Content) == 0 {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("replay: tools[%d] needs server, name, a 64-hex args_digest and content", i)})
					return
				}
			}
			replay.ToolsConsumed = 0
			if len(replay.Steps) == 0 || len(replay.Steps) > 64 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "replay: between 1 and 64 steps"})
				return
			}
			for i := range replay.Steps {
				if err := replay.Steps[i].samplingPins.validate(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("replay step %d: %v", i+1, err)})
					return
				}
			}
			replay.Consumed = 0
		}
		if !pinsSet && !replaySet {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing to set: give pins and/or replay"})
			return
		}
		e := book.merge(sub, session, pins, pinsSet, replay, replaySet)
		log.Printf("[egress-proxy sampling] %.8s… session=%.8s pins=%v replay=%v", sub, session, e.Pins != nil, e.Replay != nil)
		respond(w, session, e)
	})
	mux.HandleFunc("DELETE /privasys/sampling", func(w http.ResponseWriter, r *http.Request) {
		sub, session, ok := subjectAndSession(w, r)
		if !ok {
			return
		}
		book.del(sub, session)
		respond(w, session, nil)
	})
}
