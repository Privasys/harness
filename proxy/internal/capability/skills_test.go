// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import (
	"os"
	"path/filepath"
	"testing"
)

// A skill in the holder's Drive is theirs and has diverged from the reference
// it started as. Seeding must therefore fill gaps only: re-copying over a
// name they already have would quietly hand their assistant's behaviour back
// to the deployment, which is the opposite of the point.
func TestSeedableFillsGapsOnly(t *testing.T) {
	have := map[string]bool{"inbox-triage": true}
	got := seedable(have, []string{"inbox-triage", "learn-writing-style", ".gitignore", "meeting-prep"})
	if len(got) != 2 || got[0] != "learn-writing-style" || got[1] != "meeting-prep" {
		t.Fatalf("seedable = %v", got)
	}
	if len(seedable(map[string]bool{}, nil)) != 0 {
		t.Fatal("nothing to seed must seed nothing")
	}
}

// Pulling is content-addressed: an unchanged skill is not rewritten (so a
// quiet holder causes no local churn), and an edit made in Drive does not
// match and therefore lands.
func TestSameOnDiskRecognisesAnEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SKILL.md")
	body := []byte("# Inbox triage\n\nFile anything from my accountant under Finance.\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if !sameOnDisk(path, body) {
		t.Fatal("identical content must count as already on disk")
	}
	if sameOnDisk(path, append(body, []byte("Never draft on Sundays.\n")...)) {
		t.Fatal("an edit made in Drive must be written locally")
	}
	if sameOnDisk(filepath.Join(dir, "absent.md"), body) {
		t.Fatal("a file that is not there cannot be up to date")
	}
}
