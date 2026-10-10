package main

import (
	"net/http"
	"testing"
)

// Without a files.ai grant the harness still names the user the older way,
// and never at a host that is not Drive's through a grant.
func TestActForUserFallsBackWithoutAGrant(t *testing.T) {
	aiBroker, aiDriveHost = nil, ""
	req, _ := http.NewRequest("POST", "https://drive.example/api/v1/mcp/tools/read_file", nil)
	actForUser(req, "drive.example", "user-sub")
	if req.Header.Get("X-Privasys-On-Behalf-Of") != "user-sub" {
		t.Fatal("no grant, and the user was not named")
	}
	if got := driveAIAuthorization("drive.example", "user-sub"); got != "" {
		t.Fatalf("an AppGrant was produced with no grant: %q", got)
	}
	req2, _ := http.NewRequest("GET", "https://drive.example/", nil)
	actForUser(req2, "drive.example", "")
	if req2.Header.Get("X-Privasys-On-Behalf-Of") != "" {
		t.Fatal("a request with no user named someone")
	}
}
