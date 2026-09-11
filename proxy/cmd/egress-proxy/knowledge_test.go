// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

type memDocs struct {
	files   map[string][]byte
	refuse  bool
	loadErr error
}

func (m *memDocs) LoadNamed(sub, name string) ([]byte, bool, error) {
	if m.loadErr != nil {
		return nil, false, m.loadErr
	}
	raw, ok := m.files[sub+"/"+name]
	return raw, ok, nil
}

func (m *memDocs) SaveNamed(sub, name string, raw []byte) error {
	if m.refuse {
		return policy.ErrNotPersisted
	}
	if m.files == nil {
		m.files = map[string][]byte{}
	}
	m.files[sub+"/"+name] = raw
	return nil
}

func TestKnowledgeStoreDefaultsValidatesAndPersists(t *testing.T) {
	docs := &memDocs{}
	ks := newKnowledgeStore(docs)
	if k := ks.For("sub", "ws-1"); k.Mode != knowledgeModeAll {
		t.Fatalf("default must be all, got %+v", k)
	}
	if _, err := ks.Set("sub", "ws-1", driveKnowledge{Mode: "everything"}); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	if _, err := ks.Set("sub", "ws-1", driveKnowledge{Mode: knowledgeModeAll, Folders: []string{"n1"}}); err == nil {
		t.Fatal("folders without selected must be refused")
	}
	persisted, err := ks.Set("sub", "ws-1", driveKnowledge{Mode: knowledgeModeSelected, Folders: []string{"n2", "n1"}})
	if err != nil || !persisted {
		t.Fatalf("set: %v persisted=%v", err, persisted)
	}
	if k := ks.For("sub", "ws-1"); k.Mode != knowledgeModeSelected || strings.Join(k.Folders, ",") != "n1,n2" {
		t.Fatalf("selected folders (sorted): %+v", k)
	}
	if _, err := ks.Set("sub", "ws-2", driveKnowledge{Mode: knowledgeModeOff}); err != nil {
		t.Fatal(err)
	}
	// A fresh store reads the document back; ws-1 and ws-2 hold, ws-3 is the default.
	again := newKnowledgeStore(docs)
	if k := again.For("sub", "ws-2"); k.Mode != knowledgeModeOff {
		t.Fatalf("off did not persist: %+v", k)
	}
	if k := again.For("sub", "ws-3"); k.Mode != knowledgeModeAll {
		t.Fatalf("unknown workspace must default to all: %+v", k)
	}
	// Setting all again removes the record.
	if _, err := again.Set("sub", "ws-2", driveKnowledge{Mode: knowledgeModeAll}); err != nil {
		t.Fatal(err)
	}
	var doc knowledgeDoc
	_ = json.Unmarshal(docs.files["sub/"+knowledgeFile], &doc)
	if _, still := doc.Workspaces["ws-2"]; still {
		t.Fatal("the default must not be recorded")
	}
	// No Drive yet: the setting holds in memory and is reported unpersisted.
	noDrive := newKnowledgeStore(&memDocs{refuse: true})
	persisted, err = noDrive.Set("sub", "ws-1", driveKnowledge{Mode: knowledgeModeOff})
	if err != nil || persisted {
		t.Fatalf("no Drive: err=%v persisted=%v", err, persisted)
	}
	if k := noDrive.For("sub", "ws-1"); k.Mode != knowledgeModeOff {
		t.Fatal("an unpersisted setting must still apply for this process")
	}
}

func TestFolderIDsAreTheUsersNeverTheModels(t *testing.T) {
	args := json.RawMessage(`{"query":"acme","folder_ids":["model-picked"],"top_k":3}`)
	stripped := stripFolderIDs(args)
	if strings.Contains(string(stripped), "folder_ids") || !strings.Contains(string(stripped), `"query":"acme"`) {
		t.Fatalf("strip: %s", stripped)
	}
	narrowed := withFolderIDs(stripped, []string{"n1", "n2"})
	var obj map[string]any
	if err := json.Unmarshal(narrowed, &obj); err != nil {
		t.Fatal(err)
	}
	ids, _ := obj["folder_ids"].([]any)
	if len(ids) != 2 || ids[0] != "n1" || obj["query"] != "acme" {
		t.Fatalf("inject: %s", narrowed)
	}
	if got := string(withFolderIDs(json.RawMessage(`not json`), []string{})); !strings.Contains(got, `"folder_ids":[]`) {
		t.Fatalf("a non-object argument still gets the (empty) filter: %s", got)
	}
	if knowledgeMeta(json.RawMessage(`{"name":"read_file","arguments":{},"_meta":{"privasysSession":"session-9"}}`)) != "session-9" {
		t.Fatal("session id must come from _meta")
	}
	if knowledgeMeta(json.RawMessage(`{"name":"read_file"}`)) != "" {
		t.Fatal("no _meta means no session")
	}
	if r := knowledgeRefusal(); r["isError"] != true {
		t.Fatal("the refusal is a tool error the model can read")
	}
}

func TestWorkspaceOfSessionByMembershipThenByCwd(t *testing.T) {
	tmp := t.TempDir()
	sessions := filepath.Join(tmp, "sessions")
	wsA := filepath.Join(tmp, "workspace", "Acme")
	wsB := filepath.Join(tmp, "workspace", "Globex")
	registry := filepath.Join(tmp, "workspace.json")
	doc := `{"unit":{"name":"workspace","version":2},"global":{"initialized":true,"workspaceIds":["a","b"],"archivedSessionIds":[]},
	  "tables":{"workspaces":{
	    "a":{"path":"` + filepath.ToSlash(wsA) + `","title":"Acme","sessionIds":["s-acme"]},
	    "b":{"path":"` + filepath.ToSlash(wsB) + `","title":"Globex","sessionIds":[]}}}}`
	if err := os.WriteFile(registry, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := capability.WorkspaceOfSession(registry, sessions, "s-acme"); got != "a" {
		t.Fatalf("membership: %q", got)
	}
	// A session the registry has not accounted yet: its log names the cwd.
	dir := filepath.Join(sessions, "--project--", "s-new")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	header := `{"type":"session","version":3,"id":"s-new","createdAt":1,"isSeeded":false,"delegationDepth":0,"cwd":"` + filepath.ToSlash(filepath.Join(wsB, "sub")) + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.v3.jsonl"), []byte(header), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := capability.WorkspaceOfSession(registry, sessions, "s-new"); got != "b" {
		t.Fatalf("by cwd: %q", got)
	}
	if got := capability.WorkspaceOfSession(registry, sessions, "s-unknown"); got != "" {
		t.Fatalf("unknown session must be ungrouped: %q", got)
	}
}
