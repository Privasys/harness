// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

type fakeAgentStore struct {
	agents  []capability.AgentSpec
	written map[string]map[string][]byte
	fail    error
}

func (f *fakeAgentStore) Agents() []capability.AgentSpec { return f.agents }
func (f *fakeAgentStore) WriteAgent(name string, files map[string][]byte) (string, error) {
	if f.fail != nil {
		return "", f.fail
	}
	if f.written == nil {
		f.written = map[string]map[string][]byte{}
	}
	f.written[name] = files
	return "agents/" + name, nil
}

func callAgentsRPC(t *testing.T, st agentStore, method string, params any) map[string]any {
	t.Helper()
	prev := agentStoreFor
	agentStoreFor = func(string) agentStore {
		if st == nil {
			return nil
		}
		return st
	}
	t.Cleanup(func() { agentStoreFor = prev })
	prevSub, _ := actingSubject.Load().(string)
	actingSubject.Store("holder-1")
	t.Cleanup(func() { actingSubject.Store(prevSub) })

	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	rec := httptest.NewRecorder()
	agentsShim(rec, httptest.NewRequest(http.MethodPost, "/tool/agents/mcp", strings.NewReader(string(body))))
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("not JSON-RPC: %s", rec.Body.String())
	}
	return resp
}

// The chat writes an agent through the harness, not through Drive's tools:
// two files, checked before they are written, into agents/<name>/.
func TestWriteAgentWritesTheTwoFilesAfterCheckingThem(t *testing.T) {
	st := &fakeAgentStore{}
	resp := callAgentsRPC(t, st, "tools/call", map[string]any{"name": "write_agent", "arguments": map[string]any{
		"name":       "Inbox triage",
		"agent_md":   "# Inbox triage\n\nYou triage mail.\n",
		"agent_yaml": "prompt: Triage what arrived.\ntrigger:\n  on: mail.changes\ndebounce: 2m\n",
	}})
	out, isErr := toolText(t, resp)
	if isErr {
		t.Fatalf("refused: %v", out)
	}
	if out["written"] != "agents/Inbox triage" {
		t.Fatalf("written = %v", out["written"])
	}
	files := st.written["Inbox triage"]
	if string(files["agent.md"]) == "" || !strings.Contains(string(files["agent.yaml"]), "on: mail.changes") {
		t.Fatalf("files = %v", files)
	}
}

func TestWriteAgentRefusesABadDefinitionBeforeWriting(t *testing.T) {
	for _, c := range []struct{ name, md, yaml, want string }{
		{"../x", "persona", "prompt: p\n", "plain name"},
		{"Inbox", "", "prompt: p\n", "agent_md is empty"},
		{"Inbox", "persona", "trigger:\n  every: soonish\n", "not a valid definition"},
		{"Inbox", "persona", "trigger:\n  on: mail\n", "not a valid definition"},
	} {
		st := &fakeAgentStore{}
		out, isErr := toolText(t, callAgentsRPC(t, st, "tools/call", map[string]any{"name": "write_agent", "arguments": map[string]any{
			"name": c.name, "agent_md": c.md, "agent_yaml": c.yaml,
		}}))
		if !isErr || !strings.Contains(out["error"].(string), c.want) {
			t.Errorf("%q/%q: want a refusal mentioning %q, got %v", c.name, c.yaml, c.want, out)
		}
		if len(st.written) != 0 {
			t.Errorf("%q: a refused agent was written", c.name)
		}
	}
}

func TestAgentsToolsNeedTheHoldersWorker(t *testing.T) {
	out, isErr := toolText(t, callAgentsRPC(t, nil, "tools/call", map[string]any{"name": "list_agents", "arguments": map[string]any{}}))
	if !isErr || !strings.Contains(out["error"].(string), "not running") {
		t.Fatalf("without a mirror the tool must say what is missing: %v", out)
	}
	st := &fakeAgentStore{fail: errors.New("drive answered 403")}
	out, isErr = toolText(t, callAgentsRPC(t, st, "tools/call", map[string]any{"name": "write_agent", "arguments": map[string]any{
		"name": "Inbox", "agent_md": "persona", "agent_yaml": "prompt: p\n",
	}}))
	if !isErr || !strings.Contains(out["error"].(string), "403") {
		t.Fatalf("a Drive failure must reach the model: %v", out)
	}
}

func TestListAgentsReportsTriggerAndPause(t *testing.T) {
	st := &fakeAgentStore{agents: []capability.AgentSpec{
		{Name: "Inbox triage", Prompt: "Triage.", Trigger: capability.AgentTrigger{On: "mail.changes"}},
		{Name: "Weekly", Trigger: capability.AgentTrigger{Every: "168h"}, Paused: true},
	}}
	out, isErr := toolText(t, callAgentsRPC(t, st, "tools/call", map[string]any{"name": "list_agents", "arguments": map[string]any{}}))
	if isErr {
		t.Fatalf("refused: %v", out)
	}
	agents, _ := out["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %v", out)
	}
	first, _ := agents[0].(map[string]any)
	if first["scheduled"] != true || first["trigger"].(map[string]any)["on"] != "mail.changes" || first["min_interval"] != "10m0s" {
		t.Fatalf("first = %v", first)
	}
	second, _ := agents[1].(map[string]any)
	if second["paused"] != true || second["scheduled"] != false {
		t.Fatalf("second = %v", second)
	}
	catalogue := callAgentsRPC(t, st, "tools/list", map[string]any{})
	if !strings.Contains(catalogue["result"].(map[string]any)["tools"].([]any)[1].(map[string]any)["name"].(string), "write_agent") {
		t.Fatalf("catalogue = %v", catalogue)
	}
}
