// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Waking a holder's worker for a reminder that falls due while it is stopped.
//
// dsh's Schedule service (adopted with dsh 0.2.1) keeps its tasks in the
// worker's dsh home and fires them from a timer inside that dsh process. The
// reaper stops a worker after its idle window, which stops that timer too, so a
// reminder due while the holder is away would wait for their next visit. dsh
// already catches up on start (each recurring task contributes its latest
// missed occurrence, and a cold session is restored when delivery is due), so
// the harness only has to make sure a dsh is running when one is due.
//
// Before reaping a holder's worker the reaper reads the earliest active target
// from the Schedule store, while the holder's folder is still open, and either
// keeps the worker (due within the wake lead) or arms a timer that starts it
// again just before that target. dsh then delivers the reminder itself; the
// worker idles, is reaped again, and the next target is armed. Nothing is
// stored but one timer per subject, in memory: a process restart starts the
// approved holders' workers anyway (events.go), and their dsh catches up.
//
// Only holder workers qualify. A scratch worker's store is wiped with its
// scratch when it stops, so waking it would find nothing to deliver.

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	// reminderWakeLead is how early a stopped worker is started before a
	// reminder's target: the holder's folder open, the restore and dsh's boot.
	reminderWakeLead = 2 * time.Minute
	// reminderStaleAfter bounds keeping a worker for an overdue reminder. A
	// target further in the past than this is one dsh failed to deliver; it
	// must not keep a worker alive for ever.
	reminderStaleAfter = 10 * time.Minute
	// scheduleStoreMax caps how much of the store the proxy will read.
	scheduleStoreMax = 8 << 20
)

// scheduleStore is the part of dsh's `schedule` storage unit (storage-json's
// single layout: <dsh home>/storages/schedule.json) the wake needs.
type scheduleStore struct {
	Tables struct {
		Tasks map[string]struct {
			// Absent status decodes as active in dsh's own schema.
			Status string `json:"status"`
			Record struct {
				ScheduledAt string `json:"scheduledAt"`
			} `json:"record"`
		} `json:"tasks"`
	} `json:"tables"`
}

// nextReminder returns the earliest target among the active tasks in the
// holder's Schedule store under home. ok is false when there is no store, no
// active task, or nothing readable: the wake then simply does not arm.
func nextReminder(home string) (time.Time, bool) {
	f, err := os.Open(filepath.Join(home, "storages", "schedule.json"))
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, scheduleStoreMax+1))
	if err != nil || len(raw) > scheduleStoreMax {
		return time.Time{}, false
	}
	var store scheduleStore
	if json.Unmarshal(raw, &store) != nil {
		return time.Time{}, false
	}
	var next time.Time
	for _, task := range store.Tables.Tasks {
		if task.Status != "" && task.Status != "active" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, task.Record.ScheduledAt)
		if err != nil {
			continue
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next, !next.IsZero()
}

// reminderAction is what the reaper does with an idle holder worker whose
// earliest reminder target is due (ok false: it has none).
type reminderAction int

const (
	reminderNone reminderAction = iota // stop, arm nothing
	reminderKeep                       // due within the lead: do not stop it
	reminderArm                        // stop, and start it again before due
)

func decideReminder(now, due time.Time, ok bool) reminderAction {
	if !ok {
		return reminderNone
	}
	until := due.Sub(now)
	switch {
	case until < -reminderStaleAfter:
		return reminderNone
	case until <= reminderWakeLead:
		return reminderKeep
	default:
		return reminderArm
	}
}

// armReminderWake starts the subject's worker again reminderWakeLead before
// due, replacing any earlier timer for that subject. Starting a worker that is
// already running is a no-op (Ensure returns it), so a holder who comes back
// first costs nothing.
func (m *WorkerManager) armReminderWake(sub, key string, due time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reminderWakes == nil {
		m.reminderWakes = map[string]*time.Timer{}
	}
	if t := m.reminderWakes[sub]; t != nil {
		t.Stop()
	}
	delay := time.Until(due.Add(-reminderWakeLead))
	if delay < 0 {
		delay = 0
	}
	m.reminderWakes[sub] = time.AfterFunc(delay, func() {
		m.mu.Lock()
		delete(m.reminderWakes, sub)
		m.mu.Unlock()
		log.Printf("[workers] %s: a reminder is due at %s; starting the worker", key, due.UTC().Format(time.RFC3339))
		m.Ensure(sub)
	})
	log.Printf("[workers] %s: next reminder at %s; the worker will be started again before it", key, due.UTC().Format(time.RFC3339))
}

// reapReminder decides, for one idle worker about to be reaped, whether it
// must stay up for a reminder; it arms the wake when it may stop. It reports
// whether the worker should be kept.
func (m *WorkerManager) reapReminder(w *Worker) (keep bool) {
	if !w.holder {
		return false
	}
	due, ok := nextReminder(w.Home)
	switch decideReminder(time.Now(), due, ok) {
	case reminderKeep:
		log.Printf("[workers] %s: idle, but a reminder is due at %s; keeping it", w.Key, due.UTC().Format(time.RFC3339))
		return true
	case reminderArm:
		m.armReminderWake(w.Subject, w.Key, due)
	}
	return false
}
