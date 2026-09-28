// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"strings"
	"testing"
)

// The connectors switch is composed with one row per mounted tool, labelled
// from its name and grouped as the deployment says, and its config is JSON,
// which is YAML's flow style.
func TestConnectorsPatchListsEveryMountedTool(t *testing.T) {
	t.Setenv("HARNESS_TOOL_HOSTS", "web_search=search.example,mail=mail.example")
	t.Setenv("HARNESS_CONNECTOR_CATEGORIES", "mail=Mail, web_search=Web")
	t.Setenv("HARNESS_CONNECTOR_LABELS", "mail=Email|Gmail, Outlook or any IMAP mailbox")
	got := connectorsPatch()
	for _, want := range []string{
		"- id: privasys-connectors\n",
		`{"category":"Web","label":"Web search","server":"web_search"}`,
		`{"category":"Mail","detail":"Gmail, Outlook or any IMAP mailbox","label":"Email","server":"mail"}`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("patch lacks %q:\n%s", want, got)
		}
	}
}
