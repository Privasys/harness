// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The harness-facing half of storage consent, on the sealed ingress:
//
//	POST /privasys/capability/request   begin an ask (the UI, on a user gesture)
//	GET  /privasys/capability/status    where this holder's data is kept, truthfully
//
// Everything that authorises lives in the enclave runtime (P2 of
// plans/drive-as-remote-disk.md): the per-app sealed binding key, the pending
// ask, the wallet push, and the well-known endpoints the wallet fetches over
// RA-TLS on this app's hostname — which the runtime answers itself for any
// app whose measured manifest declares a resource. This process only relays
// the acting subject and reports what the runtime says.

import (
	"log"
	"net/http"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// withdrawnFor reports whether Drive last refused the subject's capability,
// from whichever mirror serves that subject (the process-wide one, or the
// subject's worker's).
type withdrawnFor func(sub string) bool

func registerCapabilityAPI(mux *http.ServeMux, broker *capability.Broker, withdrawn withdrawnFor) {
	// Begin an ask. Called by the harness UI over the sealed session, so the
	// relay-asserted subject names the holder this capability will belong to.
	mux.HandleFunc("POST /privasys/capability/request", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "a signed-in session is required to request storage",
			})
			return
		}
		recordSubject(sub)
		if !broker.Enabled() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "this harness is not running on the platform, so there is no Drive to connect",
			})
			return
		}
		// A previous refusal stands until the user deliberately reopens it:
		// ?retry=1 is the user changing their mind, never the app trying again.
		out, err := broker.Request(sub, r.URL.Query().Get("retry") == "1")
		if err != nil {
			log.Printf("[capability] request: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Whether this harness can persist for the acting holder, and where.
	mux.HandleFunc("GET /privasys/capability/status", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		// signed_in tells the UI whether this answer is ABOUT someone. Right
		// after sign-in the sealed session can still be anonymous for a
		// moment, and "nobody is signed in" must not render as "your Drive
		// is not connected".
		resp := map[string]any{
			"persistent": false,
			"declined":   false,
			"signed_in":  sub != "",
			"folder":     "AppData/Harness",
		}
		if sub == "" || !broker.Enabled() {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		st, err := broker.Status(sub)
		if err != nil {
			log.Printf("[capability] status: %v", err)
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp["persistent"] = st.Persistent
		resp["declined"] = st.Declined
		resp["resource_app"] = st.ResourceApp
		resp["permissions"] = st.Permissions
		// The runtime cannot see a revoke made in Drive; the mirror can, on
		// its next pass. "Granted but refused" is a state the user must see
		// as such, not as "saved to your Drive".
		if st.Persistent && withdrawn != nil && withdrawn(sub) {
			resp["withdrawn"] = true
		}
		// Drive confines app folders to AppData/<label>/ and reports the path
		// it actually chose (the label can be suffixed on collision), so once
		// granted the reported path wins over the declared label.
		if st.Label != "" {
			resp["folder"] = "AppData/" + st.Label
		}
		if p := st.Granted.Path(); p != "" {
			resp["folder"] = p
		}
		writeJSON(w, http.StatusOK, resp)
	})

	log.Printf("[capability] endpoints mounted (runtime broker enabled=%v resource=%s)", broker.Enabled(), broker.Resource())
}
