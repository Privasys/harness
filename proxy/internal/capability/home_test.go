// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package capability

import "testing"

func TestHomeCarriesOnlyWhatTheHolderMade(t *testing.T) {
	carried := []string{
		"settings.yaml",
		"storages/workspace.json",
		"cache/attachments/ab/cd.png",
		"profiles/web/cordis.patch.yml",
	}
	for _, rel := range carried {
		if !homeCarries(rel) {
			t.Errorf("%s should be carried to the holder's Drive", rel)
		}
	}
	left := []string{
		"credentials.yaml",
		"anonymous-user-id",
		"llm-deepseek/files-v3.json",
		"profiles/web/cordis.yml",
		"profiles/node_modules/@privasys/harness-bundle/cordis.patch.yml",
		"cache/other/blob",
		"../settings.yaml",
	}
	for _, rel := range left {
		if homeCarries(rel) {
			t.Errorf("%s must not be carried", rel)
		}
	}
}

func TestHomeWalkDescendsOnlyTowardsCarriedPaths(t *testing.T) {
	for _, dir := range []string{"profiles", "cache", "profiles/web"} {
		if !homeLeadsToKept(dir) {
			t.Errorf("%s leads to a carried path", dir)
		}
	}
	for _, dir := range []string{"llm-deepseek", "profiles/web/node_modules", "cache/other"} {
		if homeLeadsToKept(dir) || homeCarries(dir+"/") {
			t.Errorf("%s leads nowhere carried and should be skipped", dir)
		}
	}
}
