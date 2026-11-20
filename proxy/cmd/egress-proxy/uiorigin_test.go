// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUIOriginRedirectsPageLoadsOnThePlatformHost(t *testing.T) {
	t.Setenv("HARNESS_UI_ORIGIN", "https://ai.privasys.org")
	o := loadUIOrigin(t.Logf)

	r := httptest.NewRequest(http.MethodGet, "/sessions/abc?x=1", nil)
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("X-Privasys-Transport", "public")
	w := httptest.NewRecorder()
	if !o.redirect(w, r) {
		t.Fatal("a page load on the platform host was not redirected")
	}
	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("status %d, want 308", w.Code)
	}
	if got := w.Header().Get("Location"); got != "https://ai.privasys.org/sessions/abc?x=1" {
		t.Fatalf("Location %q", got)
	}
}

func TestUIOriginLeavesEverythingElseAlone(t *testing.T) {
	t.Setenv("HARNESS_UI_ORIGIN", "https://ai.privasys.org")
	o := loadUIOrigin(t.Logf)
	cases := []struct {
		name, method, path, dest, accept, transport string
	}{
		{"direct name", http.MethodGet, "/", "document", "", "direct"},
		{"sealed api", http.MethodPost, "/api/rpc", "", "", "sealed"},
		{"api get", http.MethodGet, "/api/events", "document", "", "public"},
		{"shell asset", http.MethodGet, "/privasys/privasys-shell.js", "script", "", "public"},
		{"bundle", http.MethodGet, "/assets/index.js", "", "text/html", "public"},
		{"image", http.MethodGet, "/favicon.svg", "image", "", "public"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if c.dest != "" {
			r.Header.Set("Sec-Fetch-Dest", c.dest)
		}
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		r.Header.Set("X-Privasys-Transport", c.transport)
		if o.redirect(httptest.NewRecorder(), r) {
			t.Errorf("%s: redirected", c.name)
		}
	}
}

func TestUIOriginRefusesAnythingButAnOrigin(t *testing.T) {
	for _, v := range []string{"", "http://ai.privasys.org", "https://ai.privasys.org/x", "https://u@ai.privasys.org", "ai.privasys.org", "https://ai.privasys.org/?q=1"} {
		t.Setenv("HARNESS_UI_ORIGIN", v)
		if o := loadUIOrigin(t.Logf); o.origin != "" {
			t.Errorf("%q accepted as %q", v, o.origin)
		}
	}
	t.Setenv("HARNESS_UI_ORIGIN", "https://ai.privasys.org/")
	if o := loadUIOrigin(t.Logf); o.origin != "https://ai.privasys.org" {
		t.Errorf("trailing slash: %q", o.origin)
	}
}
