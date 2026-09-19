// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// A workspace whose folder is gone leaves the sidebar.
//
// dsh reads its workspace registry once at boot. A folder removed from
// outside (the holder, through their Drive's window onto the folder,
// 2026-09-19) therefore stayed listed until the worker restarted. This
// watcher asks dsh itself to forget such a workspace, through the same
// unary call the sidebar's Delete makes, so dsh's registry and the disk
// agree within a tick. A folder is only judged gone on two consecutive
// ticks: dsh registers a new workspace before its folder exists for a
// moment, and a mid-creation workspace must not be taken away.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

const workspaceWatchInterval = 15 * time.Second

// watchWorkspaces runs until the worker exits.
func (m *WorkerManager) watchWorkspaces(w *Worker) {
	registry := filepath.Join(w.Home, "storages", "workspace.json")
	missing := map[string]bool{}
	t := time.NewTicker(workspaceWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-w.exited:
			return
		case <-t.C:
		}
		if !w.isReady() {
			continue
		}
		for id, dir := range capability.WorkspacePathsByID(registry) {
			if _, err := os.Stat(dir); err == nil {
				delete(missing, id)
				continue
			}
			if !missing[id] {
				missing[id] = true
				continue
			}
			delete(missing, id)
			if err := m.forgetWorkspace(w, id); err != nil {
				log.Printf("[workers] %s: workspace %s (%s) has no folder any more but dsh would not forget it: %v", w.Key, id, filepath.Base(dir), err)
				continue
			}
			log.Printf("[workers] %s: workspace %s has no folder any more; dsh forgot it", w.Key, filepath.Base(dir))
		}
	}
}

// forgetWorkspace is dsh's own workspace/delete, on the worker.
func (m *WorkerManager) forgetWorkspace(w *Worker, id string) error {
	body := fmt.Sprintf(`{"type":"client-request","rpcId":"privasys-watch","method":"workspace/delete","payload":{"args":{"workspaceId":%q}}}`, id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Upstream()+"/api/workspace/delete", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Privasys-Ingress-Token", w.Ingress)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
