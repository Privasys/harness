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
	"sync"
	"time"
)

const (
	// elicitWait bounds how long a tool call stays open for the holder's
	// answer. A form left unanswered ends the call with a readable error.
	elicitWait = 5 * time.Minute
	// elicitMaxBody bounds the JSON-RPC response carrying the answers.
	elicitMaxBody = 64 << 10
	// elicitMaxRounds bounds how many questions one tool call may ask in a
	// row (a first form, then a follow-up when something did not resolve).
	elicitMaxRounds = 3
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
// elicitPending maps an open question's id to the ask waiting for its answer.
// The answer must come back from the same holder: the id is unguessable,
// but a session is a person's, and a reply is accepted only from the worker
// that asked, never from another subject that learned the id.
var elicitPending sync.Map

type pendingElicit struct {
	ch  chan []byte
	sub string
}

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
func deliverElicitResponse(id, sub string, body []byte) bool {
	v, ok := elicitPending.Load(id)
	if !ok {
		return false
	}
	p := v.(*pendingElicit)
	if p.sub != sub {
		log.Printf("[mcp] an answer to question %s came from another subject; ignored", id)
		return false
	}
	select {
	case p.ch <- body:
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

// elicitationHeader marks the one call that carries the holder's answers,
// with the question's id. A tool app can then accept a setup value only as
// an answer to its own question and drop one a model supplied, whatever the
// skill told the model (2026-09-15: a model asked for a mail password with
// its question tool and passed it as an argument; the session record kept
// it). The shim sets it; nothing the model sends can.
const elicitationHeader = "X-Privasys-Elicitation"

// elicitAndRetry runs steps 2 to 4 for one tool call. `recall` calls the
// tool app again with the merged arguments, marked as the answers to the
// named question, and returns the raw result and status, the way callTool
// does.
func elicitAndRetry(w http.ResponseWriter, r *http.Request, reqID json.RawMessage, toolName, fn string,
	args json.RawMessage, ask elicitAsk, sessionID, sub string,
	recall func(args json.RawMessage, elicitationID string) ([]byte, int, error)) {

	sse, ok := startSSE(w)
	if !ok {
		rpcError(w, reqID, -32000, "this listener cannot stream, so the tool's question cannot be asked")
		return
	}
	final := func(text string, isErr bool) {
		_ = sse.send(map[string]any{"jsonrpc": "2.0", "id": reqID, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}})
	}
	ctx, cancel := context.WithTimeout(r.Context(), elicitWait)
	defer cancel()

	// A tool may ask AGAIN after the first answers: a mail connector asks
	// for the address and the password, infers the server, and asks for it
	// only when nothing resolves (2026-09-16). Each round is one question on
	// the stream; the answers accumulate into the arguments of the next
	// call; a bounded number of rounds keeps a confused tool from looping.
	for round := 1; round <= elicitMaxRounds; round++ {
		id := newElicitID()
		ch := make(chan []byte, 1)
		elicitPending.Store(id, &pendingElicit{ch: ch, sub: sub})

		// The client validates the request against MCP's strict wire schema
		// BEFORE its handler runs, and "password" is not one of MCP's string
		// formats (email, uri, date, date-time): sent as is, the request is
		// refused with InvalidParams and the person never sees the form
		// (first live test, 2026-09-15 17:24). So the secret marking travels
		// in _meta, which the client passes through, and the schema goes out
		// standard.
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
			elicitPending.Delete(id)
			return
		}
		log.Printf("[mcp %s] %s asks the user a question (elicitation %s, round %d); waiting", toolName, fn, id, round)

		var answer []byte
		select {
		case answer = <-ch:
		case <-ctx.Done():
			elicitPending.Delete(id)
			log.Printf("[mcp %s] elicitation %s: no answer (%v)", toolName, id, ctx.Err())
			final("the user did not answer the tool's question in time; ask whether to try again", true)
			return
		}
		elicitPending.Delete(id)

		var resp struct {
			Result *elicitResult `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(answer, &resp); err != nil || resp.Result == nil {
			// The client answered with a JSON-RPC error: it refused the
			// request (schema validation, an unsupported mode) or its handler
			// failed. No answer is in it, so the code and message may be
			// logged.
			if resp.Error != nil {
				log.Printf("[mcp %s] elicitation %s: the client refused the question: %d %s", toolName, id, resp.Error.Code, resp.Error.Message)
			} else {
				log.Printf("[mcp %s] elicitation %s: the client's reply was not an answer", toolName, id)
			}
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
		args = merged
		// The answers are in `args` from here on and go to the tool app only:
		// not logged, not echoed to the model.
		result, status, err := recall(args, id)
		if err != nil {
			final(fmt.Sprintf("tool call: %v", err), true)
			return
		}
		if next, ok := parseElicit(status, result); ok && round < elicitMaxRounds {
			ask = next
			continue
		}
		log.Printf("[mcp %s] elicitation %s: answered; %s -> %d", toolName, id, fn, status)
		final(string(result), status < 200 || status >= 300)
		return
	}
}

// liftSecrets returns the schema with `format: "password"` removed from its
// string properties, and the names of those properties in wire order. A tool
// app marks a secret that way (our contract with tool apps); the wire carries
// the mark in _meta because the client's schema would refuse it.
//
// The schema is rewritten key by key rather than through a map: the ORDER of
// the properties is the order of the form, and a map would sort it (first
// live form, 2026-09-16: "IMAP server" came before the address).
func liftSecrets(schema json.RawMessage) (json.RawMessage, []string) {
	secrets := []string{}
	top, ok := decodeOrdered(schema)
	if !ok {
		return schema, secrets
	}
	for i := range top {
		if top[i].key != "properties" {
			continue
		}
		props, ok := decodeOrdered(top[i].val)
		if !ok {
			continue
		}
		for j := range props {
			fields, ok := decodeOrdered(props[j].val)
			if !ok {
				continue
			}
			kept := fields[:0]
			for _, f := range fields {
				if f.key == "format" && string(bytes.TrimSpace(f.val)) == `"password"` {
					secrets = append(secrets, props[j].key)
					continue
				}
				kept = append(kept, f)
			}
			props[j].val = encodeOrdered(kept)
		}
		top[i].val = encodeOrdered(props)
	}
	return encodeOrdered(top), secrets
}

// orderedPair is one member of a JSON object, kept in wire order.
type orderedPair struct {
	key string
	val json.RawMessage
}

// decodeOrdered reads a JSON object into its members in wire order; false
// when the value is not an object.
func decodeOrdered(raw json.RawMessage) ([]orderedPair, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, false
	}
	var out []orderedPair
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := t.(string)
		if !ok {
			return nil, false
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false
		}
		out = append(out, orderedPair{key: key, val: val})
	}
	return out, true
}

// encodeOrdered writes the members back as one JSON object, in order.
func encodeOrdered(pairs []orderedPair) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(p.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(p.val)
	}
	b.WriteByte('}')
	return b.Bytes()
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
