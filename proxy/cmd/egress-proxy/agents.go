// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The harness's second own MCP server: the holder's agents.
//
//	/tool/agents/mcp   list_agents, write_agent
//
// An agent is a folder in the holder's working files (their holder folder,
// plan §5.9) and a workspace here (README, "Building an agent on this
// harness", contract 2). The chat builds it, and this is the tool it builds
// it with: the harness writes the two definition files itself, as root, so
// the definition is read-only for the worker (capability/agents.go). First
// live test, 2026-09-15: the skill told the model to write through Drive's
// tools, the model found none that could, and wrote the files into the
// chat's own working tree instead.
//
// Deliberately generic: the server knows the shape of an agent folder and
// nothing about what any agent does. The acting user is the calling worker's,
// as for every other tool leg.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

const (
	agentsServerName = "agents"
	// agentFileMax bounds one definition file. A persona or a definition
	// that needs more than this is not a definition any more.
	agentFileMax = 64 << 10
)

// agentStore is the little of the holder's mirror these tools need.
type agentStore interface {
	Agents() []capability.AgentSpec
	WriteAgent(name string, files map[string][]byte) (string, error)
}

// agentStoreFor resolves the acting holder's mirror. Tests replace it.
var agentStoreFor = func(sub string) agentStore {
	if workerMgr == nil {
		return nil
	}
	w := workerMgr.Get(sub)
	if w == nil {
		return nil
	}
	if s := w.Syncer(); s != nil {
		return s
	}
	return nil
}

func agentsTools() []map[string]any {
	return []map[string]any{
		{
			"name": "list_agents",
			"description": "List the user's agents: each a folder among their workspaces here, holding agent.md and agent.yaml, " +
				"with what starts each one and whether it is paused.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "write_agent",
			"description": "Create or update one of the user's agents by writing its two definition files, agent.md and agent.yaml, " +
				"into a workspace of that name among the user's working files. Agree the definition with the user in the conversation and read it back before calling. " +
				"name becomes the folder and the workspace title. agent_md is the agent's persona and standing instructions, as Markdown. " +
				"agent_yaml is its definition: prompt, trigger (exactly one of every: a duration, at: a cron schedule read in UTC, or on: <tool>.<call>), debounce, min_interval, paused; it is checked before anything is written. " +
				"The agent exists the moment the files are written; to remove an agent the user asks here, or deletes its folder from the files panel. " +
				"Never write these files anywhere else.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":       map[string]any{"type": "string", "description": "The agent's name: one plain phrase, no slashes, no leading dot, 64 characters at most."},
					"agent_md":   map[string]any{"type": "string", "description": "The full content of agent.md."},
					"agent_yaml": map[string]any{"type": "string", "description": "The full content of agent.yaml."},
				},
				"required": []string{"name", "agent_md", "agent_yaml"},
			},
		},
	}
}

// agentsShim handles one JSON-RPC message for the agents server. Same
// stateless Streamable HTTP shape as the access server.
func agentsShim(w http.ResponseWriter, r *http.Request) {
	sub := subjectOfEgress(r)
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
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
				"name":    "privasys-egress-proxy/" + agentsServerName,
				"version": "0.1.0",
			},
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		rpcResult(w, req.ID, map[string]any{})
	case "tools/list":
		tools := agentsTools()
		log.Printf("[mcp agents] catalogue: %d tool(s)", len(tools))
		rpcResult(w, req.ID, map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			rpcError(w, req.ID, -32602, "invalid params")
			return
		}
		out, isErr := callAgentsTool(p.Name, p.Arguments, sub)
		if isErr {
			log.Printf("[mcp agents] %s refused: %v", p.Name, out["error"])
		} else {
			log.Printf("[mcp agents] %s for %.8s…", p.Name, sub)
		}
		text, _ := json.Marshal(out)
		rpcResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(text)}},
			"isError": isErr,
		})
	default:
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		rpcError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// callAgentsTool runs one agents tool for sub and returns the result the
// model reads, and whether it is an error.
func callAgentsTool(name string, args json.RawMessage, sub string) (map[string]any, bool) {
	if sub == "" {
		return map[string]any{"error": "no signed-in user is bound to this session, so there is nowhere to write to"}, true
	}
	st := agentStoreFor(sub)
	if st == nil {
		return map[string]any{"error": "this user's worker is not running, so there is nowhere to write to yet"}, true
	}
	switch name {
	case "list_agents":
		return listAgents(st), false
	case "write_agent":
		var a struct {
			Name      string `json:"name"`
			AgentMD   string `json:"agent_md"`
			AgentYAML string `json:"agent_yaml"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &a); err != nil {
				return map[string]any{"error": "invalid arguments: " + err.Error()}, true
			}
		}
		return writeAgent(st, strings.TrimSpace(a.Name), a.AgentMD, a.AgentYAML)
	}
	return map[string]any{"error": fmt.Sprintf("unknown tool %q", name)}, true
}

func listAgents(st agentStore) map[string]any {
	specs := st.Agents()
	items := make([]map[string]any, 0, len(specs))
	for _, a := range specs {
		item := map[string]any{
			"name":      a.Name,
			"workspace": a.Name,
			"paused":    a.Paused,
			"scheduled": a.Scheduled(),
		}
		if a.Prompt != "" {
			item["prompt"] = a.Prompt
		}
		trigger := map[string]any{}
		if a.Trigger.Every != "" {
			trigger["every"] = a.Trigger.Every
		}
		if a.Trigger.At != "" {
			trigger["at"] = a.Trigger.At
		}
		if a.Trigger.On != "" {
			trigger["on"] = a.Trigger.On
		}
		if len(trigger) > 0 {
			item["trigger"] = trigger
		}
		debounce, minInterval := a.Durations()
		item["debounce"] = debounce.String()
		item["min_interval"] = minInterval.String()
		items = append(items, item)
	}
	return map[string]any{
		"agents": items,
		"count":  len(items),
		"note":   "each agent is a folder of that name among the user's workspaces, holding agent.md and agent.yaml; the user reads it in the files panel and changes it by asking here",
	}
}

func writeAgent(st agentStore, name, agentMD, agentYAML string) (map[string]any, bool) {
	if !capability.ValidAgentName(name) {
		return map[string]any{"error": "the agent needs a plain name: one line, no slashes, no leading dot, 64 characters at most"}, true
	}
	if strings.TrimSpace(agentMD) == "" {
		return map[string]any{"error": "agent_md is empty: the agent needs its persona and standing instructions"}, true
	}
	if len(agentMD) > agentFileMax || len(agentYAML) > agentFileMax {
		return map[string]any{"error": "a definition file is too large; keep each under 64 KiB"}, true
	}
	if _, err := capability.ParseAgentSpec([]byte(agentYAML)); err != nil {
		return map[string]any{"error": "agent_yaml is not a valid definition: " + err.Error()}, true
	}
	rel, err := st.WriteAgent(name, map[string][]byte{
		"agent.md":   []byte(agentMD),
		"agent.yaml": []byte(agentYAML),
	})
	if err != nil {
		return map[string]any{"error": "the agent could not be written: " + err.Error()}, true
	}
	return map[string]any{
		"written": rel,
		"files":   []string{"agent.md", "agent.yaml"},
		"next": "the agent exists now and its workspace appears in the sidebar at its first run. Now settle the consents it needs (section 3 of the skill), " +
			"and tell the user how to change or remove it: ask here.",
	}, false
}
