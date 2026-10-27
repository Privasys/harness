// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The agent page's half of the agents server (agents.go), on the sealed
// ingress for the browser:
//
//	GET  /privasys/agents                 the holder's agents, with their definitions
//	POST /privasys/agents/{name}/paused   {paused}: pause or resume one
//
// The page shows an agent as the chat built it: its instructions (agent.md),
// what starts it and how often it may run (agent.yaml), and whether it is
// paused. Changing what an agent does is a conversation (the agent-builder
// skill rewrites the folder); pausing is the one switch a person flips
// without one. The acting user is the relay-asserted subject, as everywhere.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// agentPageItems is listAgents' items with what the page shows beside them:
// the folder the agent's sessions run in (the page matches a session's
// working directory against it) and the two definition files.
func agentPageItems(st agentStore) []map[string]any {
	items, _ := listAgents(st)["agents"].([]map[string]any)
	specs := st.Agents()
	for i := range items {
		if i >= len(specs) {
			break
		}
		items[i]["path"] = specs[i].Path
		items[i]["instructions"] = readCapped(filepath.Join(specs[i].Path, "agent.md"), agentFileMax)
		items[i]["definition"] = readCapped(filepath.Join(specs[i].Path, "agent.yaml"), agentFileMax)
	}
	return items
}

// readCapped is a file's text, "" when it cannot be read, cut at max bytes.
func readCapped(path string, max int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)))
	if err != nil {
		return ""
	}
	return string(raw)
}

func registerAgentsAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /privasys/agents", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		st := agentStoreFor(sub)
		if st == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your working files are not open yet"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"agents": agentPageItems(st)})
	})
	mux.HandleFunc("POST /privasys/agents/{name}/paused", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		st := agentStoreFor(sub)
		if st == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "your working files are not open yet"})
			return
		}
		var body struct {
			Paused bool `json:"paused"`
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		if err != nil || json.Unmarshal(raw, &body) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {paused}"})
			return
		}
		name := r.PathValue("name")
		if err := st.SetAgentPaused(name, body.Paused); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("[agents] %.8s… %q paused=%v", sub, name, body.Paused)
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "paused": body.Paused})
	})
}
