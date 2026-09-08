// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

const timeText = "Time sampled while preparing turn 3, step 1: 2026-09-08T10:00:00Z\nElapsed since the preceding model-visible message: 12s."

func completionBody(t *testing.T, timeCtx string) []byte {
	t.Helper()
	msgs := []any{
		map[string]any{"role": "system", "content": "persona"},
		map[string]any{"role": "user", "content": "what time is it?"},
	}
	if timeCtx != "" {
		msgs = append(msgs, map[string]any{"role": "user", "content": timeCtx})
	}
	body := map[string]any{
		"model":    "qwen36-35b-a3b-fp8",
		"messages": msgs,
		"stream":   true,
		"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "web_search"}}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func completionRequest(t *testing.T, session string, raw []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9411/model/v1/chat/completions", bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	r.Header.Set("Content-Type", "application/json")
	if session != "" {
		r.Header.Set(sessionHeader, session)
	}
	return r
}

func bodyOf(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, raw)
	}
	if r.ContentLength != int64(len(raw)) {
		t.Fatalf("ContentLength %d != body %d", r.ContentLength, len(raw))
	}
	return m
}

func TestPrepareModelCall_PassesThroughWithoutPins(t *testing.T) {
	book := newSamplingBook()
	raw := completionBody(t, timeText)
	r := prepareModelCall(completionRequest(t, "s1", raw), book, "alice")
	call := modelCallOf(r)
	if call == nil {
		t.Fatal("a chat completion always carries a call record")
	}
	got := bodyOf(t, r)
	if _, ok := got["seed"]; ok {
		t.Fatal("no pins were set, yet seed was injected")
	}
	if call.TimeContext != timeText {
		t.Fatalf("time context not recorded: %q", call.TimeContext)
	}
	if call.MessagesCount != 3 || call.ToolsCount != 1 {
		t.Fatalf("shape = %d/%d", call.MessagesCount, call.ToolsCount)
	}
	if len(call.PromptDigest) != 64 {
		t.Fatalf("digest %q", call.PromptDigest)
	}
}

func TestPrepareModelCall_AppliesSessionPins(t *testing.T) {
	book := newSamplingBook()
	book.merge("alice", "s1", &samplingPins{Seed: i64(42), Temperature: f64(0.2), TopP: f64(0.9)}, true, nil, false)
	raw := completionBody(t, timeText)
	r := prepareModelCall(completionRequest(t, "s1", raw), book, "alice")
	got := bodyOf(t, r)
	if got["seed"] != float64(42) || got["temperature"] != 0.2 || got["top_p"] != 0.9 {
		t.Fatalf("pins not applied: %v", got)
	}
	call := modelCallOf(r)
	if call.Pins == nil || *call.Pins.Seed != 42 {
		t.Fatal("call record does not carry the applied pins")
	}
	// Another user's session of the same id is untouched: the book is
	// subject-keyed.
	r2 := prepareModelCall(completionRequest(t, "s1", completionBody(t, timeText)), book, "bob")
	if _, ok := bodyOf(t, r2)["seed"]; ok {
		t.Fatal("bob received alice's pins")
	}
}

func TestPromptDigest_IgnoresSampling(t *testing.T) {
	a := completionBody(t, timeText)
	var ma, mb map[string]any
	_ = json.Unmarshal(a, &ma)
	_ = json.Unmarshal(a, &mb)
	mb["seed"] = 7
	mb["temperature"] = 0.1
	mb["stream_options"] = map[string]any{"include_usage": true}
	if promptDigest(ma) != promptDigest(mb) {
		t.Fatal("sampling fields must not change the prompt digest")
	}
	mb["messages"] = append(mb["messages"].([]any), map[string]any{"role": "user", "content": "more"})
	if promptDigest(ma) == promptDigest(mb) {
		t.Fatal("a different prompt must change the digest")
	}
}

func TestNormaliseTimeContext(t *testing.T) {
	recorded := "Time sampled while preparing turn 3, step 1: RECORDED"
	// Replace in place.
	var body map[string]any
	_ = json.Unmarshal(completionBody(t, timeText), &body)
	normaliseTimeContext(body, recorded)
	if got := trailingTimeContext(body); got != recorded {
		t.Fatalf("replace: %q", got)
	}
	if n := len(body["messages"].([]any)); n != 3 {
		t.Fatalf("replace changed the message count: %d", n)
	}
	// Append when absent.
	_ = json.Unmarshal(completionBody(t, ""), &body)
	normaliseTimeContext(body, recorded)
	if got := trailingTimeContext(body); got != recorded {
		t.Fatalf("append: %q", got)
	}
	if n := len(body["messages"].([]any)); n != 3 {
		t.Fatalf("append: %d messages", n)
	}
	// Drop when the recorded step had none.
	_ = json.Unmarshal(completionBody(t, timeText), &body)
	normaliseTimeContext(body, "")
	if got := trailingTimeContext(body); got != "" {
		t.Fatalf("drop: %q", got)
	}
	if n := len(body["messages"].([]any)); n != 2 {
		t.Fatalf("drop: %d messages", n)
	}
	// A serializer that merged the prompt and the clock into one user message.
	_ = json.Unmarshal(completionBody(t, ""), &body)
	msgs := body["messages"].([]any)
	msgs[1].(map[string]any)["content"] = "what time is it?\n\n" + timeText
	normaliseTimeContext(body, recorded)
	text, _ := messageText(body["messages"].([]any)[1].(map[string]any))
	if text != "what time is it?\n\n"+recorded {
		t.Fatalf("merged: %q", text)
	}
	// Text-part content shape.
	_ = json.Unmarshal(completionBody(t, ""), &body)
	body["messages"] = append(body["messages"].([]any), map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "text", "text": timeText}},
	})
	normaliseTimeContext(body, recorded)
	if got := trailingTimeContext(body); got != recorded {
		t.Fatalf("parts: %q", got)
	}
}

func TestReplay_ConsumesMatchingStepsOnly(t *testing.T) {
	book := newSamplingBook()
	recorded := "Time sampled while preparing turn 3, step 1: RECORDED"
	var original map[string]any
	_ = json.Unmarshal(completionBody(t, recorded), &original)
	expected := promptDigest(original)
	plan := &replayPlan{
		Of: replayOf{Session: "parent", MessageID: "m9", Turn: 3},
		Steps: []replayStep{{
			samplingPins:   samplingPins{Seed: i64(99), Temperature: f64(0.5)},
			DynamicContext: "The current date and time is 2026-09-08T10:00:00Z (UTC).",
			TimeContext:    recorded,
			PromptDigest:   expected,
			MessagesCount:  3,
			ToolsCount:     1,
		}},
	}
	book.merge("alice", "child", nil, false, plan, true)

	// dsh's session-title request has another shape: it passes through, is
	// annotated as skipped, and leaves the plan armed.
	title := []byte(`{"model":"qwen36-35b-a3b-fp8","messages":[{"role":"user","content":"title this"}]}`)
	r := prepareModelCall(completionRequest(t, "child", title), book, "alice")
	call := modelCallOf(r)
	if call.Replay == nil || !call.ReplaySkipped {
		t.Fatalf("title call should be reported as skipped: %+v", call)
	}
	if _, ok := bodyOf(t, r)["seed"]; ok {
		t.Fatal("skipped step must not pin the title call")
	}
	if e := book.get("alice", "child"); e == nil || e.Replay == nil || len(e.Replay.Steps) != 1 {
		t.Fatal("plan consumed by a call of the wrong shape")
	}

	// The real step: a fresh clock, same history.
	r = prepareModelCall(completionRequest(t, "child", completionBody(t, timeText)), book, "alice")
	call = modelCallOf(r)
	got := bodyOf(t, r)
	if got["seed"] != float64(99) || got["temperature"] != 0.5 {
		t.Fatalf("step pins not applied: %v", got)
	}
	if r.Header.Get("X-Privasys-Dynamic-Context") != plan.Steps[0].DynamicContext {
		t.Fatal("dynamic context not handed back to Confidential AI")
	}
	if call.PromptMatch == nil || !*call.PromptMatch {
		t.Fatalf("normalised prompt must match the recorded digest: %+v", call)
	}
	if call.ReplayStep != 1 || call.Replay.Turn != 3 {
		t.Fatalf("replay bookkeeping: %+v", call)
	}
	if e := book.get("alice", "child"); e != nil {
		t.Fatalf("a fully consumed plan should leave no entry: %+v", e)
	}
	ann := call.annotation()
	rep, _ := ann["replay"].(map[string]any)
	if rep == nil || rep["prompt_match"] != true {
		t.Fatalf("annotation: %v", ann)
	}
}

func TestReplay_ReportsPromptMismatch(t *testing.T) {
	book := newSamplingBook()
	plan := &replayPlan{Of: replayOf{Session: "p"}, Steps: []replayStep{{
		samplingPins: samplingPins{Seed: i64(1)}, TimeContext: "Time sampled while preparing turn 1, step 1: X",
		PromptDigest: strings.Repeat("0", 64), MessagesCount: 3, ToolsCount: 1,
	}}}
	book.merge("alice", "c", nil, false, plan, true)
	r := prepareModelCall(completionRequest(t, "c", completionBody(t, timeText)), book, "alice")
	call := modelCallOf(r)
	if call.PromptMatch == nil || *call.PromptMatch {
		t.Fatal("a different prompt must be reported as a mismatch, never hidden")
	}
}

func TestReproScanBody_AnnotatesTrailer(t *testing.T) {
	call := &modelCall{RequestID: "abc", Session: "s", PromptDigest: "d", MessagesCount: 2, ToolsCount: 0}
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"reproducibility\":{\"seed\":5,\"model\":\"m\"}}\n\n" +
		"data: [DONE]\n\n"
	b := newReproScanBody(io.NopCloser(strings.NewReader(stream)), call)
	out, err := io.ReadAll(b)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(out), "\n")
	if lines[0] != "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}" {
		t.Fatalf("content frame altered: %q", lines[0])
	}
	var frame map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &frame); err != nil {
		t.Fatalf("trailer not JSON: %v: %q", err, lines[2])
	}
	repro := frame["reproducibility"].(map[string]any)
	if repro["seed"] != float64(5) {
		t.Fatal("upstream fields lost")
	}
	h, _ := repro["harness"].(map[string]any)
	if h == nil || h["request_id"] != "abc" || h["prompt_digest"] != "d" {
		t.Fatalf("annotation missing: %v", repro)
	}
	if lines[4] != "data: [DONE]" {
		t.Fatalf("sentinel altered: %q", lines[4])
	}
	// Without a call record the trailer passes byte for byte.
	b = newReproScanBody(io.NopCloser(strings.NewReader(stream)), nil)
	out, _ = io.ReadAll(b)
	if string(out) != stream {
		t.Fatal("stream altered without a call record")
	}
}

func TestSamplingAPI(t *testing.T) {
	book := newSamplingBook()
	mux := http.NewServeMux()
	registerSamplingAPI(mux, book)
	do := func(method, body string, sub string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/privasys/sampling?session=abc", strings.NewReader(body))
		if sub != "" {
			r.Header.Set("X-Privasys-Sub", sub)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := do(http.MethodGet, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET: %d", w.Code)
	}
	if w := do(http.MethodPut, `{"pins":{"temperature":5}}`, "alice"); w.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range pin accepted: %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, `{"pins":{"seed":42,"temperature":0.3}}`, "alice"); w.Code != http.StatusOK {
		t.Fatalf("PUT pins: %d %s", w.Code, w.Body)
	}
	w := do(http.MethodPut, `{"replay":{"of":{"session":"p","turn":2},"steps":[{"seed":1,"messages_count":3,"tools_count":1}]}}`, "alice")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT replay: %d %s", w.Code, w.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(do(http.MethodGet, "", "alice").Body.Bytes(), &got)
	pins, _ := got["pins"].(map[string]any)
	replay, _ := got["replay"].(map[string]any)
	if pins == nil || pins["seed"] != float64(42) || replay == nil || replay["remaining"] != float64(1) {
		t.Fatalf("GET after PUTs: %v", got)
	}
	if w := do(http.MethodPut, `{"replay":null}`, "alice"); w.Code != http.StatusOK {
		t.Fatalf("disarm: %d", w.Code)
	}
	got = nil // Unmarshal merges into a live map; start clean.
	_ = json.Unmarshal(do(http.MethodGet, "", "alice").Body.Bytes(), &got)
	if _, ok := got["replay"]; ok {
		t.Fatal("replay still armed after null")
	}
	if got["pins"] == nil {
		t.Fatal("pins lost by a replay-only PUT")
	}
	if w := do(http.MethodDelete, "", "alice"); w.Code != http.StatusOK {
		t.Fatalf("DELETE: %d", w.Code)
	}
	if e := book.get("alice", "abc"); e != nil {
		t.Fatal("entry survives DELETE")
	}
}
