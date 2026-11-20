// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"net/http"
	neturl "net/url"
	"os"
	"strings"
)

// uiOrigin is where this deployment's pages live when it has a direct
// domain (HARNESS_UI_ORIGIN, e.g. https://ai.privasys.org). A browser that
// opens a page on the platform hostname is sent there, so every link to the
// old address lands on the product's own name.
//
// Only page loads move. The wallet attests the sealed session against the
// platform hostname, and sealed frames, API calls and the shell's assets keep
// being served there. The runtime stamps X-Privasys-Transport on every
// request and hands the app the platform hostname as Host even on a direct
// name, so the leg is read from the stamp, never from Host.
type uiOrigin struct{ origin string }

// loadUIOrigin reads HARNESS_UI_ORIGIN. Anything but a bare https origin is
// ignored, with a log line, so a typo can never become an open redirect.
func loadUIOrigin(logf func(string, ...any)) uiOrigin {
	raw := strings.TrimSpace(os.Getenv("HARNESS_UI_ORIGIN"))
	if raw == "" {
		return uiOrigin{}
	}
	u, err := neturl.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		logf("[ingress] HARNESS_UI_ORIGIN %q is not an https origin; pages stay on the platform hostname", raw)
		return uiOrigin{}
	}
	return uiOrigin{origin: "https://" + u.Host}
}

// redirect answers a page load on the platform hostname with a 308 to the
// UI origin and reports whether it did.
func (o uiOrigin) redirect(w http.ResponseWriter, r *http.Request) bool {
	if o.origin == "" || !isPageLoad(r) {
		return false
	}
	if r.Header.Get("X-Privasys-Transport") == "direct" {
		return false
	}
	w.Header().Set("Location", o.origin+r.URL.RequestURI())
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusPermanentRedirect)
	return true
}

// isPageLoad is a browser navigation: a GET or HEAD for a document, outside
// the API and the shell's own asset paths.
func isPageLoad(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := r.URL.Path
	for _, prefix := range []string{"/api/", "/privasys/", "/assets/", "/plugins/"} {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "document":
		return true
	case "":
		// Older browsers send no fetch metadata: fall back to what the
		// client says it accepts.
		return strings.Contains(r.Header.Get("Accept"), "text/html")
	default:
		return false
	}
}
