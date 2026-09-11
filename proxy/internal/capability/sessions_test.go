// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectKeyMatchesDsh(t *testing.T) {
	cases := map[string]string{
		"/data/users/c5144d4b012d351c/workspace/Chat": "--data-users-c5144d4b012d351c-workspace-Chat--",
		"C:\\Users\\x y": "--C-Users-x~0020y--",
		"/":              "--root--",
		"/a//b":          "--a-b--",
		"/tilde~/é":      "--tilde~007E-~00E9--",
		"":               noCwdProjectDir,
	}
	for cwd, want := range cases {
		if got := projectKey(cwd); got != want {
			t.Errorf("projectKey(%q) = %q, want %q", cwd, got, want)
		}
	}
	if !isLegacySlug("--data-users-x-workspace-Chat--") || !isLegacySlug(noCwdProjectDir) || isLegacySlug("Chat") || isLegacySlug("--") {
		t.Fatal("isLegacySlug misclassifies")
	}
}

func writeSession(t *testing.T, root, project, id, cwd string) string {
	t.Helper()
	dir := filepath.Join(root, project, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	header := `{"type":"session","version":3,"id":"` + id + `","createdAt":1,"isSeeded":false,"delegationDepth":0`
	if cwd != "" {
		header += `,"cwd":"` + cwd + `"`
	}
	header += "}\n"
	if err := os.WriteFile(filepath.Join(dir, "session.v3.jsonl"), []byte(header+`{"type":"x"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeRegistry(t *testing.T, dir string, doc string) string {
	t.Helper()
	p := filepath.Join(dir, "workspace.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRemoteRelUsesWorkspaceTitleAndArchive(t *testing.T) {
	tmp := t.TempDir()
	sessions := filepath.Join(tmp, "sessions")
	workspace := filepath.Join(tmp, "workspace")
	s := NewSyncer(nil, nil, "", "app", sessions, workspace)
	chat := filepath.Join(workspace, "Chat")
	acme := filepath.Join(workspace, "acme")
	other := filepath.Join(workspace, "other", "acme")
	s.SetRegistryFile(writeRegistry(t, tmp, `{
	  "unit": {"name": "workspace", "version": 2},
	  "global": {"initialized": true, "workspaceIds": ["w1"], "archivedSessionIds": ["s-archived"]},
	  "tables": {"workspaces": {
	    "w1": {"path": "`+filepath.ToSlash(chat)+`", "title": "Chat", "sessionIds": []},
	    "w2": {"path": "`+filepath.ToSlash(acme)+`", "title": "Acme", "sessionIds": []},
	    "w3": {"path": "`+filepath.ToSlash(other)+`", "title": "Acme", "sessionIds": []}
	  }}
	}`))
	reg := s.registry()
	if !reg.archived["s-archived"] || len(reg.workspaces) != 3 {
		t.Fatalf("registry not read: %+v", reg)
	}

	live := writeSession(t, sessions, projectKey(chat), "s-live", filepath.ToSlash(chat))
	archived := writeSession(t, sessions, projectKey(chat), "s-archived", filepath.ToSlash(chat))
	deeper := writeSession(t, sessions, projectKey(filepath.Join(chat, "sub")), "s-deep", filepath.ToSlash(filepath.Join(chat, "sub")))
	loose := writeSession(t, sessions, projectKey(filepath.Join(workspace, "Scratch")), "s-loose", filepath.ToSlash(filepath.Join(workspace, "Scratch")))
	nocwd := writeSession(t, sessions, noCwdProjectDir, "s-nocwd", "")
	outside := writeSession(t, sessions, projectKey("/dsh"), "s-out", "/dsh")
	dup1 := writeSession(t, sessions, projectKey(acme), "s-acme", filepath.ToSlash(acme))
	dup2 := writeSession(t, sessions, projectKey(other), "s-other", filepath.ToSlash(other))

	want := map[string]string{
		filepath.Join(live, "session.v3.jsonl"):     "sessions/Chat/s-live/session.v3.jsonl",
		filepath.Join(archived, "session.v3.jsonl"): "sessions/Archived/Chat/s-archived/session.v3.jsonl",
		filepath.Join(deeper, "session.v3.jsonl"):   "sessions/Chat/sub/s-deep/session.v3.jsonl",
		filepath.Join(loose, "session.v3.jsonl"):    "sessions/Scratch/s-loose/session.v3.jsonl",
		filepath.Join(nocwd, "session.v3.jsonl"):    "sessions/No workspace/s-nocwd/session.v3.jsonl",
		filepath.Join(outside, "session.v3.jsonl"):  "sessions/dsh/s-out/session.v3.jsonl",
		filepath.Join(dup1, "session.v3.jsonl"):     "sessions/Acme (acme)/s-acme/session.v3.jsonl",
		filepath.Join(dup2, "session.v3.jsonl"):     "sessions/Acme (" + shortID("w3") + ")/s-other/session.v3.jsonl",
	}
	// Two "Acme" workspaces whose directories are both called acme: the
	// second falls back to its id. The first keeps the directory form only
	// while no other workspace shares that basename, so both use the id.
	want[filepath.Join(dup1, "session.v3.jsonl")] = "sessions/Acme (" + shortID("w2") + ")/s-acme/session.v3.jsonl"
	for local, exp := range want {
		if got := s.remoteRel(reg, local); got != exp {
			t.Errorf("remoteRel(%s) = %q, want %q", local, got, exp)
		}
	}
	// A directory whose log is unreadable keeps its local name.
	torn := filepath.Join(sessions, projectKey(chat), "s-torn")
	if err := os.MkdirAll(torn, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(torn, "session.v3.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.remoteRel(reg, filepath.Join(torn, "session.v3.jsonl")); got != "sessions/"+projectKey(chat)+"/s-torn/session.v3.jsonl" {
		t.Errorf("torn header: %q", got)
	}
}

func TestSanitiseName(t *testing.T) {
	cases := map[string]string{
		"Chat":          "Chat",
		" a/b\\c ":      "a-b-c",
		"...":           "Untitled",
		"--x--":         "Workspace --x--",
		"line\nbreak":   "linebreak",
		noCwdProjectDir: "Workspace " + noCwdProjectDir,
	}
	for in, want := range cases {
		if got := sanitiseName(in); got != want {
			t.Errorf("sanitiseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadRegistryMissingIsEmpty(t *testing.T) {
	reg := loadRegistry(filepath.Join(t.TempDir(), "absent.json"))
	if len(reg.archived) != 0 || len(reg.workspaces) != 0 {
		t.Fatal("a missing registry must read as empty")
	}
	s := NewSyncer(nil, nil, "", "app", t.TempDir(), t.TempDir())
	if got := s.displayFor(reg, ""); got != noWorkspaceName {
		t.Fatalf("no cwd: %q", got)
	}
}
