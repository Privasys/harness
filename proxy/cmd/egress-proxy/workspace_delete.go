// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Deleting a workspace deletes it.
//
// dsh's "Delete workspace" removes the registration and keeps the directory
// and the session logs, which is right for a tool over somebody's own disk.
// Here the directory is the holder's working files in their folder, and a
// person who deletes a workspace means the files to go (2026-09-19: "the
// delete didn't fully work"). So the ingress watches dsh's own call and, once
// dsh has confirmed the deletion, removes the directory and the session logs
// that named it. Never an agent's directory: an agent is removed by asking
// the chat, which knows what else depends on it.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

const workspaceDeletePath = "/api/workspace/delete"

// serveWorkspaceDelete forwards dsh's delete call and, on its ok, removes
// what dsh kept. The workspace's path is read from dsh's registry BEFORE the
// call, since the registration is gone after it.
func serveWorkspaceDelete(w http.ResponseWriter, r *http.Request, rp *httputil.ReverseProxy, worker *Worker) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		http.Error(w, "body", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Payload struct {
			Args struct {
				WorkspaceID string `json:"workspaceId"`
			} `json:"args"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(body, &envelope)
	registry := filepath.Join(worker.Home, "storages", "workspace.json")
	dir := capability.WorkspacePathByID(registry, envelope.Payload.Args.WorkspaceID)

	rec := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
	rp.ServeHTTP(rec, r)
	if rec.status != http.StatusOK || dir == "" {
		return
	}
	var answer struct {
		Result struct {
			OK bool `json:"ok"`
		} `json:"result"`
	}
	if json.Unmarshal(rec.body.Bytes(), &answer) != nil || !answer.Result.OK {
		return
	}
	worker.removeWorkspaceFiles(dir)
}

// removeWorkspaceFiles removes a deleted workspace's directory and its session
// logs, when the directory is one of this worker's own working trees.
func (w *Worker) removeWorkspaceFiles(dir string) {
	rel, err := filepath.Rel(w.Workspace, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		log.Printf("[workers] %s: deleted workspace %s is not under the working tree; its files are left alone", w.Key, dir)
		return
	}
	if s := w.Syncer(); s != nil && s.IsAgentDir(strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]) {
		log.Printf("[workers] %s: deleted workspace %s is an agent's; its files are left alone (an agent is removed by asking the chat)", w.Key, dir)
		return
	}
	sessions := capability.SessionDirsFor(w.Sessions, dir)
	for _, sdir := range sessions {
		if err := os.RemoveAll(sdir); err != nil {
			log.Printf("[workers] %s: session logs of %s: %v", w.Key, dir, err)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("[workers] %s: deleted workspace %s: %v", w.Key, dir, err)
		return
	}
	log.Printf("[workers] %s: deleted workspace %s: directory and %d session log(s) removed", w.Key, filepath.Base(dir), len(sessions))
}

// recordingWriter passes a response through and keeps a copy of its body.
type recordingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recordingWriter) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recordingWriter) Write(b []byte) (int, error) {
	if r.body.Len() < 1<<20 {
		r.body.Write(b)
	}
	return r.ResponseWriter.Write(b)
}
