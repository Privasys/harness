// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// A second declared resource, and the shape for every one after it.
//
//	POST /privasys/capability/{resource}/request   begin an ask (user gesture)
//	GET  /privasys/capability/{resource}/status    what this holder approved
//
// The storage leg keeps its own unprefixed paths (capability_api.go) because
// the UI already calls them and its status answer is Drive-shaped: a folder,
// an AppData path, a withdrawal the mirror noticed. None of that means
// anything for a mailbox, and forcing one answer to describe both is how a
// screen ends up saying "your mail is stored in AppData/Mail Connector",
// which is the opposite of what the connector does.
//
// So this is deliberately the GENERIC surface: it reports what the runtime
// knows — approved, declined, which resource service, which permissions, and
// whatever opaque result that service returned — and lets the caller phrase
// it. The next connector adds a manifest entry and a broker, and nothing here
// changes.

import (
	"log"
	"net/http"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// resourceLeg is one declared resource the holder can be asked about.
type resourceLeg struct {
	// name matches the `resources` entry in the measured manifest. A
	// mismatch is not a configuration error the runtime reports; the ask is
	// simply refused with "resource not declared in the app manifest".
	name   string
	broker *capability.Broker
}

func registerResourceCapabilityAPI(mux *http.ServeMux, legs ...resourceLeg) {
	byName := make(map[string]*capability.Broker, len(legs))
	for _, l := range legs {
		if l.name != "" && l.broker != nil {
			byName[l.name] = l.broker
		}
	}
	if len(byName) == 0 {
		return
	}

	mux.HandleFunc("POST /privasys/capability/{resource}/request", func(w http.ResponseWriter, r *http.Request) {
		broker, sub, ok := legFor(w, r, byName)
		if !ok {
			return
		}
		recordSubject(sub)
		// A refusal stands until the holder deliberately reopens it: retry=1
		// is the person changing their mind, never the app asking again.
		out, err := broker.Request(sub, r.URL.Query().Get("retry") == "1")
		if err != nil {
			log.Printf("[capability %s] request: %v", r.PathValue("resource"), err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("GET /privasys/capability/{resource}/status", func(w http.ResponseWriter, r *http.Request) {
		// signed_in tells the UI whether this answer is ABOUT someone. Just
		// after sign-in the sealed session can still be anonymous for a
		// moment, and "nobody is signed in" must not render as "you have not
		// approved this".
		sub := r.Header.Get("X-Privasys-Sub")
		resp := map[string]any{
			"resource":  r.PathValue("resource"),
			"approved":  false,
			"declined":  false,
			"signed_in": sub != "",
		}
		broker := byName[r.PathValue("resource")]
		if broker == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "this app declares no such resource",
			})
			return
		}
		if sub == "" || !broker.Enabled() {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		st, err := broker.Status(sub)
		if err != nil {
			// Reported as "not approved" rather than as an error: the caller
			// is rendering a state, and a broker hiccup must not read to the
			// holder as a revocation.
			log.Printf("[capability %s] status: %v", r.PathValue("resource"), err)
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp["approved"] = st.Persistent
		resp["declined"] = st.Declined
		resp["kind"] = st.Kind
		resp["permissions"] = st.Permissions
		resp["resource_app"] = st.ResourceApp
		if st.Granted != nil && st.Granted.ServiceResult != nil {
			// Opaque to this process and forwarded as-is. For a mailbox the
			// connector puts the account it approved here, which is the one
			// thing a holder needs to see to know WHICH mailbox they linked.
			resp["service_result"] = st.Granted.ServiceResult
		}
		writeJSON(w, http.StatusOK, resp)
	})

	for name, b := range byName {
		log.Printf("[capability] resource %q mounted (runtime broker enabled=%v)", name, b.Enabled())
	}
}

// legFor resolves the resource and the acting holder, or writes the refusal.
func legFor(w http.ResponseWriter, r *http.Request, byName map[string]*capability.Broker) (*capability.Broker, string, bool) {
	broker := byName[r.PathValue("resource")]
	if broker == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "this app declares no such resource",
		})
		return nil, "", false
	}
	// The relay-asserted subject, so the capability belongs to the person at
	// the keyboard rather than to whoever the request claims to act for.
	sub := r.Header.Get("X-Privasys-Sub")
	if sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "a signed-in session is required to approve this",
		})
		return nil, "", false
	}
	if !broker.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "this harness is not running on the platform, so there is nothing to approve",
		})
		return nil, "", false
	}
	return broker, sub, true
}
