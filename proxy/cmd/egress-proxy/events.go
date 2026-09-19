// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// The runtime's event stream, consumed once for the whole process: an
// approval, a denial, a revoke, a holder folder opened or closed. Nothing
// here polls. What an event does:
//
//   - every broker forgets its cached status of that subject, so the next
//     status answer is the runtime's, not a ten-second-old one;
//   - anyone long-polling the browser status for that subject is released
//     (capability_api.go), so the row updates the moment the wallet answers;
//   - an approval of the storage resource starts the holder's worker, so
//     their agents load and their routines arm without a visit;
//   - a revoke of the holder folder stops the holder's worker, so the
//     runtime's close has nothing left to kill.
//
// A runtime without the stream ends the loop; the browser then falls back
// to its slow refresh and routines still arm on the holder's next visit.

// subjectNotifier releases whoever waits on a subject when an event about
// them arrives.
type subjectNotifier struct {
	mu      sync.Mutex
	waiters map[string]chan struct{}
}

func newSubjectNotifier() *subjectNotifier {
	return &subjectNotifier{waiters: map[string]chan struct{}{}}
}

// Wait blocks until an event about sub arrives, ctx ends or the timeout
// passes. It reports whether an event arrived.
func (n *subjectNotifier) Wait(ctx context.Context, sub string, timeout time.Duration) bool {
	n.mu.Lock()
	ch, ok := n.waiters[sub]
	if !ok {
		ch = make(chan struct{})
		n.waiters[sub] = ch
	}
	n.mu.Unlock()
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	case <-time.After(timeout):
		return false
	}
}

// Notify releases the waiters of sub.
func (n *subjectNotifier) Notify(sub string) {
	n.mu.Lock()
	if ch, ok := n.waiters[sub]; ok {
		close(ch)
		delete(n.waiters, sub)
	}
	n.mu.Unlock()
}

// followRuntimeEvents runs the loop for the life of the process.
func followRuntimeEvents(ctx context.Context, stream *capability.Broker, legs []resourceLeg, storageName string, mgr *WorkerManager, notify *subjectNotifier) {
	if stream == nil || !stream.Enabled() {
		return
	}
	stream.Events(ctx, func(ev capability.Event) {
		log.Printf("[events] %s %s for %.8s…", ev.Type, ev.Resource, ev.Subject)
		for _, l := range legs {
			l.broker.Forget(ev.Subject)
		}
		notify.Notify(ev.Subject)
		if mgr == nil || ev.Subject == "" {
			return
		}
		switch {
		case ev.Type == "capability.approved" && ev.Resource == storageName:
			// A worker running without its mirror (the previous grant was
			// withdrawn in Drive) is replaced: the fresh one restores from
			// the new grant, and dsh only reads its workspaces at boot.
			if w := mgr.Get(ev.Subject); w != nil {
				if s := w.Syncer(); s != nil && !s.Ticking() {
					log.Printf("[events] %.8s… approved their Drive again: restarting the worker on it", ev.Subject)
					go func() { mgr.Stop(w); mgr.Ensure(ev.Subject) }()
					return
				}
			}
			mgr.Ensure(ev.Subject)
		case ev.Type == "capability.revoked" && ev.Resource == holderResourceKindName(legs):
			if w := mgr.Get(ev.Subject); w != nil {
				log.Printf("[events] %.8s… revoked their folder: stopping the worker", ev.Subject)
				go mgr.Stop(w)
			}
		}
	})
	log.Printf("[events] the runtime offers no event stream; status is read on demand")
}

// holderResourceKindName is the declared name of the app_storage resource,
// or "" when none.
func holderResourceKindName(legs []resourceLeg) string {
	for _, l := range legs {
		if l.kind == holderResourceKind {
			return l.name
		}
	}
	return ""
}

// rearmRoutines starts a worker for every holder the runtime lists as
// approved, so their agents load and their routines arm after a restart
// without anybody opening the harness. The runtime is the record; this
// process keeps none.
func rearmRoutines(storage *capability.Broker, mgr *WorkerManager) {
	if storage == nil || !storage.Enabled() || mgr == nil {
		return
	}
	subjects, err := storage.Subjects()
	if err != nil {
		log.Printf("[routines] approved subjects: %v (routines arm on each holder's next visit)", err)
		return
	}
	n := 0
	for _, s := range subjects {
		if s.Status == "approved" {
			mgr.Ensure(s.Subject)
			n++
		}
	}
	if n > 0 {
		log.Printf("[routines] %d approved holder(s) listed by the runtime: their workers start and their routines arm", n)
	}
}
