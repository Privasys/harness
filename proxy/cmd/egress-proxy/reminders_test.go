// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSchedule writes a Schedule store the way dsh's storage-json single
// layout does: one document with the unit header and the tasks table.
func writeSchedule(t *testing.T, doc string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "storages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schedule.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestNextReminderPicksEarliestActive(t *testing.T) {
	home := writeSchedule(t, `{
  "unit": {"name": "schedule", "version": 1},
  "global": null,
  "tables": {"tasks": {
    "a": {"sessionId": "s1", "status": "active", "record": {"id": "a", "scheduledAt": "2026-10-20T09:00:00.000Z"}},
    "b": {"sessionId": "s1", "status": "inactive", "record": {"id": "b", "scheduledAt": "2026-10-15T09:00:00.000Z"}},
    "c": {"sessionId": "s2", "record": {"id": "c", "scheduledAt": "2026-10-18T07:30:00.000Z"}}
  }}
}`)
	got, ok := nextReminder(home)
	if !ok {
		t.Fatal("expected a next reminder")
	}
	// c has no status (dsh decodes that as active) and is earlier than a; b
	// is earlier still but stopped, so it must not count.
	want := time.Date(2026, 10, 18, 7, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}

func TestNextReminderNothingToWakeFor(t *testing.T) {
	cases := map[string]string{
		"only inactive": `{"tables":{"tasks":{"x":{"status":"inactive","record":{"scheduledAt":"2026-10-20T09:00:00.000Z"}}}}}`,
		"empty table":   `{"tables":{"tasks":{}}}`,
		"malformed":     `{"tables":`,
		"bad instant":   `{"tables":{"tasks":{"x":{"record":{"scheduledAt":"tomorrow"}}}}}`,
	}
	for name, doc := range cases {
		if _, ok := nextReminder(writeSchedule(t, doc)); ok {
			t.Errorf("%s: armed a wake for nothing", name)
		}
	}
	if _, ok := nextReminder(t.TempDir()); ok {
		t.Error("no store: armed a wake for nothing")
	}
}

func TestDecideReminder(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		due  time.Time
		ok   bool
		want reminderAction
	}{
		{"no reminder", time.Time{}, false, reminderNone},
		{"far ahead: stop and arm", now.Add(3 * time.Hour), true, reminderArm},
		{"just past the lead: arm", now.Add(reminderWakeLead + time.Second), true, reminderArm},
		{"within the lead: keep", now.Add(reminderWakeLead), true, reminderKeep},
		{"due now: keep", now, true, reminderKeep},
		{"recently overdue: keep, dsh is delivering", now.Add(-time.Minute), true, reminderKeep},
		// A target long past is one dsh failed to deliver; it must not keep a
		// worker up for ever.
		{"stale: stop", now.Add(-reminderStaleAfter - time.Second), true, reminderNone},
	}
	for _, c := range cases {
		if got := decideReminder(now, c.due, c.ok); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// A scratch worker's store is wiped with its scratch, so it is never kept or
// woken, whatever its store says.
func TestReapReminderIgnoresScratchWorkers(t *testing.T) {
	home := writeSchedule(t, `{"tables":{"tasks":{"x":{"record":{"scheduledAt":"`+
		time.Now().Add(30*time.Second).UTC().Format(time.RFC3339Nano)+`"}}}}}`)
	m := &WorkerManager{}
	if m.reapReminder(&Worker{Subject: "s", Key: "k", Home: home, holder: false}) {
		t.Fatal("kept a scratch worker for a reminder")
	}
	if len(m.reminderWakes) != 0 {
		t.Fatal("armed a wake for a scratch worker")
	}
	// The same store on a holder worker, due within the lead, keeps it.
	if !m.reapReminder(&Worker{Subject: "s", Key: "k", Home: home, holder: true}) {
		t.Fatal("did not keep a holder worker whose reminder is due within the lead")
	}
}

func TestArmReminderWakeReplacesEarlierTimer(t *testing.T) {
	m := &WorkerManager{}
	m.armReminderWake("s", "k", time.Now().Add(5*time.Hour))
	first := m.reminderWakes["s"]
	m.armReminderWake("s", "k", time.Now().Add(6*time.Hour))
	if len(m.reminderWakes) != 1 || m.reminderWakes["s"] == first {
		t.Fatal("a second arm must replace the first timer, not add one")
	}
	if first.Stop() {
		t.Fatal("the replaced timer was still pending")
	}
	m.reminderWakes["s"].Stop()
}
