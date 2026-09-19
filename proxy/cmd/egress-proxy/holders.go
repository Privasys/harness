// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// The holder's folder: one directory of this app's volume, encrypted with
// the holder's own key, that the runtime opens for their worker under their
// consent and locks when the worker stops. When it is open it is the
// worker's root (workspace and dsh home); this process never sees the key
// and never names the path, the runtime does. Without it (no declaration, a
// runtime without holder folders, an approval not given yet) the worker
// runs on the scratch, which is wiped at stop and at boot.
const holderResourceKind = "app_storage"

// holderBroker returns the broker of the declared app_storage resource, or
// nil when the deployment declares none.
func holderBroker(decls []resourceDecl, legs []resourceLeg) *capability.Broker {
	for _, d := range decls {
		if d.Kind != holderResourceKind {
			continue
		}
		for _, l := range legs {
			if l.name == d.Name && l.broker.Enabled() {
				return l.broker
			}
		}
	}
	return nil
}

// openHolderFolder asks the runtime for the worker's holder folder and, when
// it is open, moves the worker's roots into it. Any other answer leaves the
// scratch in place and is logged once per start.
func (m *WorkerManager) openHolderFolder(w *Worker) {
	if m.holders == nil || w.Subject == systemSubject {
		return
	}
	hf, err := m.holders.OpenHolderFolder(w.Subject, w.UID)
	if err != nil {
		log.Printf("[workers] %s: holder folder: %v (running on the scratch)", w.Key, err)
		return
	}
	switch hf.Status {
	case "open":
		// Everything the agent works with lives here (plan §5.9): the
		// working trees and the agents among them, the dsh home, the
		// holder's skills, and the sessions (an agent's runs stay; the
		// conversations are also mirrored to the holder's Drive).
		w.Dir = hf.Path
		w.Workspace = filepath.Join(hf.Path, "workspace")
		w.Home = filepath.Join(hf.Path, "dsh-home")
		w.Skills = filepath.Join(hf.Path, "skills")
		w.Sessions = filepath.Join(hf.Path, "sessions")
		w.holder = true
		log.Printf("[workers] %s: holder folder open at %s", w.Key, hf.Path)
	case "needs_holder":
		log.Printf("[workers] %s: holder folder needs the holder's approval (ask %.8s… on %s); running on the scratch", w.Key, hf.Nonce, hf.AppHost)
	default:
		log.Printf("[workers] %s: holder folder %s; running on the scratch", w.Key, hf.Status)
	}
}

// closeHolderFolder locks the folder after the worker's process is gone.
func (m *WorkerManager) closeHolderFolder(w *Worker) {
	if m.holders == nil || !w.holder {
		return
	}
	busy, err := m.holders.CloseHolderFolder(w.Subject)
	switch {
	case err != nil:
		log.Printf("[workers] %s: holder folder close: %v", w.Key, err)
	case busy:
		log.Printf("[workers] %s: holder folder still in use; the runtime keeps it open until the files are released", w.Key)
	default:
		log.Printf("[workers] %s: holder folder locked", w.Key)
	}
}

// seedSkills copies the deployment's reference skills into the holder's
// skills directory once: only when that directory does not exist yet. After
// that the directory is theirs; a reference skill they removed stays
// removed, one they changed stays changed. The reference set itself is still
// on dsh's skill path (PRIVASYS_SKILL_DIRS), so a holder who deletes
// everything keeps the deployment's behaviour. Returns how many files were
// copied.
func seedSkills(dir, seeds string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	if _, err := os.Stat(dir); err == nil {
		return 0, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	n := 0
	err := filepath.WalkDir(seeds, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rerr := filepath.Rel(seeds, p)
		if rerr != nil || rel == "." {
			return nil
		}
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if werr := os.WriteFile(target, data, 0o600); werr != nil {
			return werr
		}
		n++
		return nil
	})
	return n, err
}
