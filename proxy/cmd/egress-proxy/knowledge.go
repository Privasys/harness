// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Drive knowledge, per workspace.
//
// The user's files reach the agent through Drive's own MCP server, and
// Drive gates that globally: the folders the user enabled for AI. A harness
// runs several workspaces for one user, and a session about one customer
// must not surface another customer's files, so each workspace carries a
// narrower setting:
//
//	off       the Drive tools answer nothing for this workspace
//	all       every folder the user enabled for AI (Drive's scope, unchanged)
//	selected  a subset of those folders, chosen in the harness
//
// The setting is the user's own document (workspaces.json, beside their
// tenant policy on their Drive) and is enforced here, on the measured leg
// every tool call crosses: the shim resolves the calling session's
// workspace, and either refuses the call (off) or names the folder set to
// Drive as `folder_ids`, which Drive intersects with the AI scope. The model
// never sees the field and cannot widen it: whatever it sends under that
// name is dropped before the call goes out.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

const (
	knowledgeModeOff      = "off"
	knowledgeModeAll      = "all"
	knowledgeModeSelected = "selected"
	// knowledgeFile is the document's name at the root of the granted folder.
	knowledgeFile = "workspaces.json"
	// knowledgeTool is the Drive tool mount name (/tool/drive/mcp).
	knowledgeTool     = "drive"
	maxKnowledgeBytes = 256 << 10
)

// knowledge is the process-wide store, set at startup (main.go); nil off
// the platform, where no Drive exists and the shim narrows nothing.
var knowledge *knowledgeStore

// driveKnowledge is one workspace's setting.
type driveKnowledge struct {
	Mode    string   `json:"mode"`
	Folders []string `json:"folders,omitempty"`
}

func (k driveKnowledge) valid() error {
	switch k.Mode {
	case knowledgeModeOff, knowledgeModeAll, knowledgeModeSelected:
	default:
		return fmt.Errorf("mode must be %q, %q or %q", knowledgeModeOff, knowledgeModeAll, knowledgeModeSelected)
	}
	if k.Mode != knowledgeModeSelected && len(k.Folders) > 0 {
		return errors.New("folders are only meaningful with mode \"selected\"")
	}
	for _, f := range k.Folders {
		if strings.TrimSpace(f) == "" {
			return errors.New("a folder id is empty")
		}
	}
	return nil
}

// knowledgeDoc is the user's document: one setting per workspace id.
type knowledgeDoc struct {
	Workspaces map[string]driveKnowledge `json:"workspaces"`
}

// namedDocs is the per-user document storage the tenant backend already
// provides for the policy, opened to a second file name.
type namedDocs interface {
	LoadNamed(sub, name string) ([]byte, bool, error)
	SaveNamed(sub, name string, raw []byte) error
}

// knowledgeStore caches each user's document for the life of the process
// and writes through to their Drive.
type knowledgeStore struct {
	docs namedDocs
	mu   sync.Mutex
	byID map[string]*knowledgeDoc // by subject
}

func newKnowledgeStore(docs namedDocs) *knowledgeStore {
	return &knowledgeStore{docs: docs, byID: map[string]*knowledgeDoc{}}
}

// doc returns the user's document, loading it once. A missing or unreadable
// document reads as empty: every workspace then has the default, "all".
func (s *knowledgeStore) doc(sub string) *knowledgeDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.byID[sub]; ok {
		return d
	}
	d := &knowledgeDoc{Workspaces: map[string]driveKnowledge{}}
	if s.docs != nil {
		if raw, found, err := s.docs.LoadNamed(sub, knowledgeFile); err == nil && found {
			var loaded knowledgeDoc
			if json.Unmarshal(raw, &loaded) == nil && loaded.Workspaces != nil {
				d = &loaded
			}
		} else if err != nil {
			log.Printf("[knowledge] %.8s…: load: %v", sub, err)
		}
	}
	s.byID[sub] = d
	return d
}

// For returns the setting of one workspace; "all" when none is recorded or
// the session belongs to no workspace.
func (s *knowledgeStore) For(sub, workspaceID string) driveKnowledge {
	if sub == "" || workspaceID == "" {
		return driveKnowledge{Mode: knowledgeModeAll}
	}
	d := s.doc(sub)
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := d.Workspaces[workspaceID]; ok && k.valid() == nil {
		return k
	}
	return driveKnowledge{Mode: knowledgeModeAll}
}

// Set records one workspace's setting and writes the document through.
// persisted is false when the user has no Drive connected yet: the setting
// then holds for this process only, and the caller must say so.
func (s *knowledgeStore) Set(sub, workspaceID string, k driveKnowledge) (persisted bool, err error) {
	if err := k.valid(); err != nil {
		return false, err
	}
	if k.Mode == knowledgeModeSelected {
		sort.Strings(k.Folders)
	}
	d := s.doc(sub)
	s.mu.Lock()
	if k.Mode == knowledgeModeAll {
		delete(d.Workspaces, workspaceID) // the default needs no record
	} else {
		d.Workspaces[workspaceID] = k
	}
	raw, merr := json.MarshalIndent(d, "", "  ")
	s.mu.Unlock()
	if merr != nil {
		return false, merr
	}
	if s.docs == nil {
		return false, nil
	}
	if err := s.docs.SaveNamed(sub, knowledgeFile, raw); err != nil {
		if errors.Is(err, policy.ErrNotPersisted) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---- session → workspace -------------------------------------------------------

// workspaceOfSession resolves the workspace a session belongs to for one
// user, from dsh's own registry: membership first, then the session log's
// working directory against the workspace paths. "" when the session is
// ungrouped or unknown.
func workspaceOfSession(sub, sessionID string) string {
	if workerMgr == nil || sub == "" || sessionID == "" {
		return ""
	}
	w := workerMgr.Get(sub)
	if w == nil {
		return ""
	}
	return capability.WorkspaceOfSession(filepath.Join(w.Home, "storages", "workspace.json"), w.Sessions, sessionID)
}

// ---- the shim's hook --------------------------------------------------------------

// knowledgeMeta reads the calling session from the MCP request's _meta
// (set by the harness's own MCP client, never by the model).
func knowledgeMeta(params json.RawMessage) string {
	var p struct {
		Meta struct {
			Session string `json:"privasysSession"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return ""
	}
	return p.Meta.Session
}

// stripFolderIDs drops any folder_ids the model put in its arguments: the
// narrowing is the user's setting, never the model's request.
func stripFolderIDs(args json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil || obj == nil {
		return args
	}
	if _, ok := obj["folder_ids"]; !ok {
		return args
	}
	delete(obj, "folder_ids")
	out, err := json.Marshal(obj)
	if err != nil {
		return args
	}
	return out
}

// withFolderIDs names the folder set on the outgoing arguments.
func withFolderIDs(args json.RawMessage, folders []string) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil || obj == nil {
		obj = map[string]json.RawMessage{}
	}
	ids, _ := json.Marshal(folders)
	obj["folder_ids"] = ids
	out, err := json.Marshal(obj)
	if err != nil {
		return args
	}
	return out
}

// knowledgeRefusal is the tool result the model reads when a workspace has
// Drive knowledge off: a sentence it can relay, not a protocol error.
func knowledgeRefusal() map[string]any {
	msg, _ := json.Marshal(map[string]any{
		"error":   "drive knowledge is off for this workspace",
		"message": "The user has switched Drive knowledge off for this workspace, so their Drive files are not available here. Answer without them, or tell the user they can change this in the workspace's Drive knowledge setting.",
	})
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(msg)}},
		"isError": true,
	}
}

// ---- the UI's endpoints ------------------------------------------------------

// availableFolder is one folder the user may narrow a workspace to, as
// Drive's ai-scope listing reports it.
type availableFolder struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Always bool   `json:"always,omitempty"`
}

// fetchAIScope asks Drive, acting for the user, for the roots of their AI
// scope: the picker's source of truth.
func fetchAIScope(r *http.Request, client *http.Client, driveHost, sub string) ([]availableFolder, bool, error) {
	if driveHost == "" {
		return nil, false, errors.New("no Drive host configured")
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://"+driveHost+"/api/v1/mcp/ai-scope", nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("X-Privasys-On-Behalf-Of", sub)
	decorateSpend(req, sub)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("Drive answered HTTP %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var out struct {
		Folders   []availableFolder `json:"folders"`
		AllScoped bool              `json:"all_scoped"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false, err
	}
	return out.Folders, out.AllScoped, nil
}

func registerKnowledgeAPI(mux *http.ServeMux, ks *knowledgeStore, client *http.Client, driveHost string) {
	mux.HandleFunc("GET /privasys/workspaces/{id}/drive-knowledge", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		id := r.PathValue("id")
		k := ks.For(sub, id)
		out := map[string]any{"workspace_id": id, "mode": k.Mode, "folders": k.Folders}
		if k.Folders == nil {
			out["folders"] = []string{}
		}
		folders, allScoped, err := fetchAIScope(r, client, driveHost, sub)
		if err != nil {
			// The setting is still shown and saveable; only the picker's
			// choices are missing, and the UI says why.
			out["available_error"] = err.Error()
		} else {
			if folders == nil {
				folders = []availableFolder{}
			}
			out["available"] = folders
			out["all_scoped"] = allScoped
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("PUT /privasys/workspaces/{id}/drive-knowledge", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		id := r.PathValue("id")
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxKnowledgeBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var k driveKnowledge
		if err := json.Unmarshal(raw, &k); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {mode, folders}"})
			return
		}
		persisted, err := ks.Set(sub, id, k)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("[knowledge] %.8s… workspace %.8s…: mode=%s folders=%d persisted=%v", sub, id, k.Mode, len(k.Folders), persisted)
		out := map[string]any{"workspace_id": id, "mode": k.Mode, "folders": k.Folders, "persisted": persisted}
		if k.Folders == nil {
			out["folders"] = []string{}
		}
		if !persisted {
			out["notice"] = "this setting applies now but is not saved anywhere: connect your Drive to keep it"
		}
		writeJSON(w, http.StatusOK, out)
	})
}
