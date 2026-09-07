// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The requesting-app half of the wallet's resource-capability protocol.
//
//	GET  /.well-known/privasys/capability-request?nonce=…   what the holder is being asked
//	POST /.well-known/privasys/capability-result            the outcome, quoting the nonce
//
// Both are fetched by the wallet over RA-TLS, so the far end has attested this
// enclave before reading a byte. That is the point of putting the binding key
// HERE rather than in the push: the push carries only a nonce and a host, and
// a key that rode it could be anyone's while the identity on the holder's
// screen was genuine.
//
// A third, harness-facing route starts the flow:
//
//	POST /privasys/capability/request   (sealed session; begins an ask)
//
// which the UI calls when the user chooses to enable storage.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// driveAppID names the resource service by IDENTITY, so the wallet resolves it
// rather than following a URL out of our payload. Sourced from the measured
// image environment; it is also a declared dependency, so the same identity is
// pinned in OID 7.1.
func driveAppID() string {
	return envOr("HARNESS_DRIVE_APP_ID", "cf7a0d585468416884c341ebe0ce4025")
}

// storageFolder is the folder the harness asks for. A NAME, never a path with
// an ownership boundary in it: Drive derives the tenant from the authenticated
// holder and refuses any request that names one.
func storageFolder() string { return envOr("HARNESS_STORAGE_FOLDER", "Harness") }

func registerCapabilityAPI(mux *http.ServeMux, store *capability.Store) {
	// What the holder is being asked. Served to the wallet inside RA-TLS.
	mux.HandleFunc("GET /.well-known/privasys/capability-request", func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		p, ok := store.Get(nonce)
		if !ok {
			// Unknown or expired. Deliberately indistinguishable: telling a
			// caller which of the two it is would let them probe for live
			// nonces.
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "no such capability request",
			})
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	// The outcome. Approved or denied — denial is delivered too, so the app
	// stops asking rather than re-prompting forever.
	mux.HandleFunc("POST /.well-known/privasys/capability-result", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Nonce         string            `json:"nonce"`
			Status        string            `json:"status"`
			CapabilityID  string            `json:"capability_id"`
			ServiceResult map[string]string `json:"service_result"`
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil || json.Unmarshal(raw, &body) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}
		if body.Status != "approved" && body.Status != "denied" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be approved or denied"})
			return
		}
		// The nonce is the authorisation here. This route is on a publicly
		// reachable mux and carries no holder credential by design — the
		// wallet must not hand this enclave one. What bounds the damage is
		// that a forged result can only ever point us at coordinates we
		// cannot use: the capability it names is bound to OUR public key, and
		// a grant we do not hold simply fails on first use. So a bogus POST
		// costs a failed write and a log line, not access.
		//
		// The first real use of a capability is therefore also its
		// verification, and that is where a mismatch surfaces.
		g, err := store.Resolve(body.Nonce, body.Status, body.CapabilityID, body.ServiceResult)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": g.Status})
	})

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
		existing := store.Granted(sub)
		if existing.Usable() {
			writeJSON(w, http.StatusOK, map[string]any{
				"status":        "already_granted",
				"capability_id": existing.CapabilityID,
			})
			return
		}
		// A previous refusal stands until the user deliberately reopens it.
		// Delivering deny exists so the app stops asking; re-prompting on the
		// next page load would make the decision meaningless. ?retry=1 is the
		// user changing their mind, never the app trying again.
		if existing.Denied() && r.URL.Query().Get("retry") != "1" {
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "declined",
				"at":     existing.At,
			})
			return
		}
		p, err := store.Create(sub, driveAppID(), storageFolder(),
			// A VALUE, not a sentence: the wallet composes the prose and
			// translates it into 25 locales. Prose supplied here could not be
			// translated, and an app that writes the words on the approval
			// screen can describe itself however it likes.
			storageFolder(),
			[]string{capability.PermRead, capability.PermWrite})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// The push carries ONLY the nonce and this host. Everything else the
		// wallet learns from the attested fetch above.
		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "pending",
			"nonce":    p.Nonce,
			"app_host": os.Getenv("HARNESS_PUBLIC_HOST"),
		})
	})

	// Whether this harness can persist for the acting holder, so the UI can
	// show the memory-only banner honestly.
	mux.HandleFunc("GET /privasys/capability/status", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		g := store.Granted(sub)
		writeJSON(w, http.StatusOK, map[string]any{
			"persistent":   g.Usable(),
			"declined":     g.Denied(),
			"resource_app": driveAppID(),
			"folder":       storageFolder(),
		})
	})

	log.Printf("[capability] endpoints mounted (resource_app=%s folder=%s)", driveAppID(), storageFolder())
}
