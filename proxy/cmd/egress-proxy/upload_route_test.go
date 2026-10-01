// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"net/http/httptest"
	"testing"
)

func TestUploadKeepsItsByteType(t *testing.T) {
	req := httptest.NewRequest("POST", dshFileUploadPath+"?sessionId=s1&name=a.pdf", nil)
	req.Header.Set("Content-Type", "application/json")
	restoreUploadContentType(req)
	if got := req.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("upload content type = %q, want application/octet-stream", got)
	}
}

func TestOtherRoutesKeepTheRelayType(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/session/prompt"},
		{"GET", dshFileUploadPath},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Content-Type", "application/json")
		restoreUploadContentType(req)
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s %s content type = %q, want it untouched", c.method, c.path, got)
		}
	}
}
