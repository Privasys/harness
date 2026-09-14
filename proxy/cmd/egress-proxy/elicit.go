// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// MCP elicitation through the shim (Bertrand, 2026-09-14: "do the
// elicitation dev; the form handles secrets properly; the content won't be
// in the session").
//
// A tool app that needs the holder's answer before it can act (a mailbox
// connector without a credential yet) must not have the model collect it:
// the model would carry the answer in its context and the session log would
// keep it. MCP has the standard step for this, elicitation: the SERVER asks
// the CLIENT for structured input against a JSON schema, the client's own
// question surface collects it, and the answer travels back to the server.
// The model sees the tool's final result and nothing else.
//
// The tool apps sit behind this stateless shim over plain REST, so the shim
// is where the standard step is spoken:
//
//   1. tools/call → the tool app answers HTTP 428 with
//      {"elicit": {"message": "...", "requestedSchema": {...}}}
//   2. the shim turns the POST's response into an event stream and sends
//      the JSON-RPC request elicitation/create (with the schema, the message,
//      and _meta naming the session and the server, so dsh routes the form
//      to the right conversation and can say who is asking)
//   3. dsh shows the form on its question surface and POSTs the JSON-RPC
//      RESPONSE ({"id", "result": {"action", "content"}}) to this same URL
//   4. the shim matches it, calls the tool app again with the answers merged
//      into the arguments, and writes the final tools/call result on the
//      stream it kept open
//
// Nothing here names a product: any tool app may answer 428 with a schema,
// and a string property with `format: "password"` is masked by the surface.
// The answers are never logged, never reach the worker's model leg, and are
// forwarded once, to the tool app that asked.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

const (
	// elicitWait bounds how long a tool call stays open for the holder's
	// answer. A form left unanswered ends the call with a readable error.
	elicitWait = 5 * time.Minute
	// elicitMaxBody bounds the JSON-RPC response carrying the answers.
	elicitMaxBody = 64 << 10
)

// elicitAsk is what a tool app answers 428 with.
type elicitAsk struct {
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requestedSchema"`
}

// elicitResult is the client's answer (MCP ElicitResult).
type elicitResult struct {
	Action  string          `json:"action"` // accept | decline | cancel
	Content json.RawMessage `json:"content"`
}

// elicitPending holds the open asks: elicitation id → the channel its
// answer arrives on. One entry per open tool call.
var elicitPending sync.Map

// parseElicit reports whether a tool app's answer is an elicitation ask.
func parseElicit(status int, body []byte) (elicitAsk, bool) {
	if status != http.StatusPreconditionRequired {
		return elicitAsk{}, false
	}
	var wrapped struct {
		Elicit *elicitAsk `json:"elicit"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil || wrapped.Elicit == nil || len(wrapped.Elicit.RequestedSchema) == 0 {
		return elicitAsk{}, false
	}
	return *wrapped.Elicit, true
}

// isRPCResponse reports whether a JSON-RPC message is a response (no method,
// an id, a result or an error): the client answering an elicitation.
func isRPCResponse(body []byte) (string, bool) {
	var m struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Method != "" || len(m.ID) == 0 {
		return "", false
	}
	if len(m.Result) == 0 && len(m.Error) == 0 {
		return "", false
	}
	var id string
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return "", false // elicitation ids are strings this shim minted
	}
	return id, true
}

// deliverElicitResponse hands the client's answer to the waiting tool call.
func deliverElicitResponse(id string, body []byte) bool {
	v, ok := elicitPending.Load(id)
	if !ok {
		return false
	}
	ch := v.(chan []byte)
	select {
	case ch <- body:
		return true
	default:
		return false // already answered
	}
}

func newElicitID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "elicit-" + hex.EncodeToString(b)
}

// sseWriter writes JSON-RPC messages as server-sent events and flushes each.
type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func startSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) send(msg any) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: message\ndata: %s\n\n", raw); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// elicitAndRetry runs steps 2 to 4 for one tool call. `recall` calls the
// tool app again with the merged arguments and returns the raw result and
// status, the way callTool does.
func elicitAndRetry(w http.ResponseWriter, r *http.Request, reqID json.RawMessage, toolName, fn string,
	args json.RawMessage, ask elicitAsk, sessionID string,
	recall func(args json.RawMessage) ([]byte, int, error)) {

	sse, ok := startSSE(w)
	if !ok {
		rpcError(w, reqID, -32000, "this listener cannot stream, so the tool's question cannot be asked")
		return
	}
	id := newElicitID()
	ch := make(chan []byte, 1)
	elicitPending.Store(id, ch)
	defer elicitPending.Delete(id)

	// The client validates the schema strictly, and "password" is not one
	// of MCP's string formats, so the secret marking travels in _meta
	// (which the client passes through) and the schema goes out standard.
	schema, secrets := liftSecrets(ask.RequestedSchema)
	params := map[string]any{
		"message":         ask.Message,
		"requestedSchema": schema,
		"_meta": map[string]any{
			"privasysSession": sessionID,
			"privasysServer":  toolName,
			"privasysTool":    fn,
			"privasysSecrets": secrets,
		},
	}
	if err := sse.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "elicitation/create", "params": params}); err != nil {
		return
	}
	log.Printf("[mcp %s] %s asks the user a question (elicitation %s); waiting", toolName, fn, id)

	final := func(text string, isErr bool) {
		_ = sse.send(map[string]any{"jsonrpc": "2.0", "id": reqID, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}})
	}

	ctx, cancel := context.WithTimeout(r.Context(), elicitWait)
	defer cancel()
	var answer []byte
	select {
	case answer = <-ch:
	case <-ctx.Done():
		log.Printf("[mcp %s] elicitation %s: no answer (%v)", toolName, id, ctx.Err())
		final("the user did not answer the tool's question in time; ask whether to try again", true)
		return
	}

	var resp struct {
		Result *elicitResult   `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(answer, &resp); err != nil || resp.Result == nil {
		final("the user's answer could not be read; ask whether to try again", true)
		return
	}
	switch resp.Result.Action {
	case "accept":
	case "decline":
		log.Printf("[mcp %s] elicitation %s: declined", toolName, id)
		final("the user declined to answer the tool's question; do not ask again unless they say so", true)
		return
	default:
		log.Printf("[mcp %s] elicitation %s: cancelled", toolName, id)
		final("the user cancelled the tool's question; ask whether to try again later", true)
		return
	}
	merged, err := mergeArgs(args, resp.Result.Content)
	if err != nil {
		final("the user's answer could not be applied: "+err.Error(), true)
		return
	}
	// The answers are in `merged` from here on and go to the tool app only:
	// not logged, not echoed to the model.
	result, status, err := recall(merged)
	if err != nil {
		final(fmt.Sprintf("tool call: %v", err), true)
		return
	}
	log.Printf("[mcp %s] elicitation %s: answered; %s -> %d", toolName, id, fn, status)
	final(string(result), status < 200 || status >= 300)
}

// liftSecrets returns the schema with `format: "password"` removed from its
// string properties, and the names of those properties. A tool app marks a
// secret that way (our contract); the wire carries the mark in _meta.
func liftSecrets(schema json.RawMessage) (json.RawMessage, []string) {
	secrets := []string{}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return schema, secrets
	}
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p, _ := props[name].(map[string]any)
		if f, _ := p["format"].(string); f == "password" {
			delete(p, "format")
			secrets = append(secrets, name)
		}
	}
	out, err := json.Marshal(s)
	if err != nil {
		return schema, secrets
	}
	return out, secrets
}

// mergeArgs lays the answers over the model's arguments; an answer wins.
func mergeArgs(args, content json.RawMessage) (json.RawMessage, error) {
	base := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &base); err != nil {
			return nil, fmt.Errorf("arguments: %w", err)
		}
	}
	answers := map[string]any{}
	if len(bytes.TrimSpace(content)) > 0 {
		if err := json.Unmarshal(content, &answers); err != nil {
			return nil, fmt.Errorf("answers: %w", err)
		}
	}
	for k, v := range answers {
		base[k] = v
	}
	return json.Marshal(base)
}
