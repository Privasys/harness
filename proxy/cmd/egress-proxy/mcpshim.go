// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// MCP shim: /tool/{name}/mcp serves the Model Context Protocol (Streamable
// HTTP, stateless JSON responses) in front of a platform tool app's
// privasys_http surface (GET /api/v1/mcp/tools + POST /api/v1/mcp/tools/{fn}).
//
// Why here and not a dsh plugin: dsh's stock mcp-client (official MCP SDK)
// then needs only CONFIG to mount an attested tool app — no Node code — and
// the tool catalogue and every call cross the measured Go leg, where the
// declared-dependency gate has already vetted the peer (D2). The caller's
// Authorization header is forwarded verbatim; the shim holds no credentials.

const mcpProtocolVersion = "2025-03-26"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func rpcError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
}

// upstreamTool is one entry of the privasys_http catalogue.
type upstreamTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// mcpShim handles one JSON-RPC message for the tool app at host.
func mcpShim(w http.ResponseWriter, r *http.Request, client *http.Client, toolName, host string) {
	if r.Method == http.MethodGet {
		// No server-initiated stream in the stateless shim.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodDelete {
		// Session teardown: nothing to tear down.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		rpcError(w, nil, -32700, "parse error")
		return
	}

	switch req.Method {
	case "initialize":
		rpcResult(w, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "privasys-egress-proxy/" + toolName,
				"version": "0.1.0",
			},
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		rpcResult(w, req.ID, map[string]any{})
	case "tools/list":
		tools, err := fetchCatalogue(r, client, host)
		if err != nil {
			// This was SILENT, and it is the failure that matters most: the
			// catalogue is how a tool acquires its capabilities, so a refusal
			// here mounts an EMPTY tool set and the agent simply reports
			// having no such tool — indistinguishable from one nobody
			// configured. Three rounds of "why is there no Drive tool?" came
			// down to this line not existing.
			log.Printf("[mcp %s] CATALOGUE FAILED against %s: %v — the tool will mount with no capabilities",
				toolName, host, err)
			rpcError(w, req.ID, -32000, fmt.Sprintf("catalogue: %v", err))
			return
		}
		log.Printf("[mcp %s] catalogue: %d tool(s) from %s", toolName, len(tools), host)
		out := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object"}`)
			}
			out = append(out, map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": schema,
			})
		}
		rpcResult(w, req.ID, map[string]any{"tools": out})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			rpcError(w, req.ID, -32602, "invalid params")
			return
		}
		args := p.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		args = applyDocumentedDefaults(toolName, p.Name, args)
		result, status, price, err := callTool(r, client, host, p.Name, args, "")
		if err != nil {
			rpcError(w, req.ID, -32000, fmt.Sprintf("tool call: %v", err))
			return
		}
		// A priced tool: the runtime refused with the exact attested price.
		// Consent is the USER's standing policy, never the model's answer;
		// when it admits this price the call is retried with the byte-exact
		// header, and the charge is recorded against the session. When it
		// does not, the model is told why in words it can relay.
		if status == http.StatusPaymentRequired {
			credits := parseCredits(price, string(result))
			if credits == 0 {
				rpcResult(w, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": string(result)}},
					"isError": true,
				})
				return
			}
			ok, why := spendConsent(toolName, p.Name, credits)
			if !ok {
				msg, _ := json.Marshal(map[string]any{
					"error":   "payment approval required",
					"tool":    toolName + "/" + p.Name,
					"price":   priceHeaderValue(credits),
					"message": why,
				})
				rpcResult(w, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": string(msg)}},
					"isError": true,
				})
				return
			}
			result, status, _, err = callTool(r, client, host, p.Name, args, priceHeaderValue(credits))
			if err != nil {
				rpcError(w, req.ID, -32000, fmt.Sprintf("tool call: %v", err))
				return
			}
			if status >= 200 && status < 300 {
				spendCharged(toolName, p.Name, credits)
			}
		}
		// Non-2xx upstreams surface as tool errors the model can read,
		// not protocol errors that abort the loop.
		rpcResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(result)}},
			"isError": status < 200 || status >= 300,
		})
	default:
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted) // unknown notification
			return
		}
		rpcError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// documentedDefaults fills argument fields a tool DOCUMENTS as defaulting but
// whose implementation rejects when the field is absent (schema/impl mismatch
// on the tool side). Concretely: web-search-brave's `count` says "Pass 0 for
// the default of 10" yet errors on a missing count — and a small model at low
// reasoning effort omits it, costing a visible failed call per attempt.
// Interim robustness shim; the durable fix is the tool app accepting absence
// (queued for the next fleet-tools release — a tool rebuild rotates its
// MRENCLAVE and cascades dependency re-pins, so it rides that window).
var documentedDefaults = map[string]map[string]map[string]any{
	"web_search": {
		"search":     {"count": 0},
		"search-raw": {"count": 0},
	},
}

// applyDocumentedDefaults returns args with any missing documented-default
// fields filled in; args pass through untouched when no defaults apply.
func applyDocumentedDefaults(server, tool string, args json.RawMessage) json.RawMessage {
	defaults := documentedDefaults[server][tool]
	if len(defaults) == 0 {
		return args
	}
	var parsed map[string]any
	if err := json.Unmarshal(args, &parsed); err != nil || parsed == nil {
		return args
	}
	changed := false
	for field, value := range defaults {
		if _, present := parsed[field]; !present {
			parsed[field] = value
			changed = true
		}
	}
	if !changed {
		return args
	}
	filled, err := json.Marshal(parsed)
	if err != nil {
		return args
	}
	return filled
}

func fetchCatalogue(r *http.Request, client *http.Client, host string) ([]upstreamTool, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		"https://"+host+"/api/v1/mcp/tools", nil)
	if err != nil {
		return nil, err
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	// The catalogue fetch is the one assistant request tool apps serve
	// without an acting user (it runs on mcp-client's startup timer), but
	// naming the subject when one is bound is harmless and consistent.
	if sub := currentSubject(); sub != "" {
		req.Header.Set("X-Privasys-On-Behalf-Of", sub)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var payload struct {
		Tools []upstreamTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse catalogue: %w", err)
	}
	return payload.Tools, nil
}

// callTool performs one tool invocation. approved, when set, is the
// byte-exact fee consent (`N credits`) the runtime hosting the tool expects;
// the returned price is the runtime's X-Billing-Price on a refusal.
func callTool(r *http.Request, client *http.Client, host, fn string, args json.RawMessage, approved string) (json.RawMessage, int, string, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"https://"+host+"/api/v1/mcp/tools/"+fn, bytes.NewReader(args))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	// Name the acting user for user-scoped tool apps (Drive requires it on
	// every tool call) and, since 2026-09-08, the PAYER of a priced call.
	// The subject is the relay-asserted sign-in identity recorded by the
	// ingress front — never anything the model supplied.
	if sub := currentSubject(); sub != "" {
		req.Header.Set("X-Privasys-On-Behalf-Of", sub)
	}
	// The consent header is set by THIS layer from the user's policy, never
	// copied from what dsh or the model sent: the model could otherwise
	// approve a fee on the user's behalf simply by asking.
	req.Header.Del("X-Billing-Approved")
	if approved != "" {
		req.Header.Set("X-Billing-Approved", approved)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, "", err
	}
	return raw, resp.StatusCode, resp.Header.Get("X-Billing-Price"), nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
