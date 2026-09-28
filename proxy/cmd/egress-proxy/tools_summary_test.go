// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"strings"
	"testing"
)

// A tool is shown with the app its host proved to be, and until it has
// proved anything, with no app at all rather than a guess.
func TestAttestedToolsNameTheAppEachHostProved(t *testing.T) {
	t.Setenv("HARNESS_CONNECTOR_LABELS", "calendar=Calendar|Google, Outlook or any CalDAV calendar")
	proved := map[string]string{"calendar-connector.apps.test.privasys.org": "07a002a2a136474696b5a5b540b604d6"}
	appOf := func(host string) string { return proved[strings.ToLower(host)] }
	got := attestedTools(map[string]string{
		"calendar":   "calendar-connector.apps.test.privasys.org",
		"web_search": "web-search-brave.apps.privasys.org",
	}, appOf)
	if len(got) != 2 || got[0].Name != "calendar" || got[0].Label != "Calendar" ||
		got[0].AppID != "07a002a2a136474696b5a5b540b604d6" {
		t.Fatalf("calendar: %+v", got)
	}
	if got[1].Label != "Web search" || got[1].AppID != "" {
		t.Fatalf("a host that has proved nothing yet carries no app: %+v", got[1])
	}
}
