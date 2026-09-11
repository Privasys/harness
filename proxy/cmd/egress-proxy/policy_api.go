// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The verification API: how a user checks which policy this enclave is
// applying to them.
//
// Why an API is sufficient, and a per-user certificate is not needed: once the
// enforcing code is measured, in-session assertions inherit the measurement's
// integrity. A measured binary cannot tell a user their policy is X while
// applying Y — that would take different code, which changes the hash. The
// certificate buys one extra property the API does not, TRANSFERABILITY, and
// that is why the CEILING's digest is on the leaf (see stamp.go): a third
// party can verify the product's posture without an account. A tenant's own
// policy needs no such proof, because the only party who needs it is the
// tenant, and they are inside an attested sealed session when they ask.
//
// Two audiences, two exposures:
//
//   - **Anonymous** (this mux is publicly reachable, as /privasys/attestation
//     already is): the ceiling and its digest, so anyone can fetch the text,
//     hash it, and compare against the leaf. Nothing tenant-specific.
//   - **Authenticated** (X-Privasys-Sub, asserted by the sealed relay and
//     stripped from client input by the enclave manager, so it is never caller
//     -supplied): additionally the caller's own policy and the effective
//     resolution.
//
// Writes require a subject for the same reason. This mux answers the public
// internet, so an unauthenticated PUT would let anyone install a policy.

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// maxPolicyBytes bounds a submitted document. Policies are small; anything
// larger is a mistake or an attempt to exhaust the enclave's memory.
const maxPolicyBytes = 256 << 10

// fillSubject sets `subject` on a tenant document that omits it. The document
// is re-serialised only in that case; a document that already names a
// subject is stored byte for byte.
func fillSubject(raw []byte, sub string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return raw
	}
	if v, ok := m["subject"]; ok && len(v) > 0 && string(v) != `""` {
		return raw
	}
	m["subject"] = json.RawMessage(strconv.Quote(sub))
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return raw
	}
	return out
}

// registerPolicyAPI mounts the verification and tenant-write endpoints.
func registerPolicyAPI(mux *http.ServeMux, store *policy.Store, stamp *stamper) {
	mux.HandleFunc("GET /privasys/policy", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		// Bind the acting subject here too. recordSubject otherwise fires only
		// for requests proxied through to dsh, so a session that read or set
		// its policy before touching the app would leave the forward path
		// resolving against the ceiling alone — the tenant's own narrowing
		// silently not applied until some unrelated request bound them.
		recordSubject(sub)
		eff := store.Effective(sub)
		out := map[string]any{
			"subject":           redactSubject(sub),
			"summary":           eff.Summarise(),
			"policy_digest_oid": OIDPolicyDigest,
			// The ceiling's exact bytes, so a verifier can hash them and
			// compare with the leaf without trusting this JSON envelope.
			"ceiling_document": json.RawMessage(eff.Ceiling.Raw()),
		}
		if eff.Tenant != nil {
			out["tenant_document"] = json.RawMessage(eff.Tenant.Raw())
		}
		writeJSON(w, http.StatusOK, out)
	})

	// A tenant installs or replaces their own policy. It may only narrow the
	// ceiling; the store rejects a widening document at the write so the user
	// gets an immediate, comprehensible error rather than a policy that
	// silently does less than it says.
	mux.HandleFunc("PUT /privasys/policy/tenant", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			// Anonymous callers reach this mux from the public internet.
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "a signed-in session is required to set a policy",
			})
			return
		}
		recordSubject(sub)
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxPolicyBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// The panel never learns the raw subject (the GET redacts it), so a
		// document it submits may omit `subject`; the acting subject is the
		// only value that could ever be right there, and the store still
		// refuses a document naming anyone else.
		raw = fillSubject(raw, sub)
		d, persisted, err := store.SaveTenant(sub, raw)
		if err != nil {
			// Policy errors are user-facing and actionable — surface the
			// sentence, not a generic 400.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		out := map[string]any{
			"digest":  d.Digest(),
			"summary": store.Effective(sub).Summarise(),
			// The document lives in the holder's Drive folder. Before a Drive
			// is connected it applies for this process only, and the UI must
			// say so rather than imply the enclave kept it.
			"persisted": persisted,
		}
		if !persisted {
			out["notice"] = "this policy applies now but is not saved anywhere: connect your Drive to keep it"
		}
		writeJSON(w, http.StatusOK, out)
	})

	// What this harness ACTUALLY reached, as distinct from what it permits.
	// The panel shows both lines; showing the posture alone is how an honest
	// product acquires a false badge, in either direction.
	//
	// Gated on the subject, unlike the ceiling: which hosts an agent fetched
	// is a record of what its user was doing, and this mux answers the public
	// internet. The posture is public because it is a property of the product;
	// the behaviour is not.
	mux.HandleFunc("GET /privasys/egress-log", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Privasys-Sub") == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "a signed-in session is required to read this harness's egress record",
			})
			return
		}
		writeJSON(w, http.StatusOK, audit.Snapshot(50))
	})

	// The service ceiling. This is the app OWNER's tier — Privasys, or an
	// enterprise's admins — and it is authorised by the platform's configure
	// surface (manager authorizeConfigure: a bearer carrying
	// <aud>:app:<hex>:owner|admin), not by anything this process decides.
	//
	// ⚠ NOT YET WIRED to that authorisation: the manager gates the app's
	// declared configure endpoint, and pointing it here is a manifest change
	// that must ship with the app, not a code change alone. Until then this
	// route is DISABLED rather than exposed unauthenticated, because the mux
	// is publicly reachable and an open ceiling write is a total bypass of
	// every tier below it.
	mux.HandleFunc("PUT /privasys/policy/ceiling", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "the service ceiling is set through the platform configure surface (owner/admin), not this endpoint",
		})
	})
}

// redactSubject reports whether a caller is authenticated without echoing a
// pairwise identifier back over a publicly reachable endpoint.
func redactSubject(sub string) string {
	if sub == "" {
		return ""
	}
	if len(sub) <= 8 {
		return "…"
	}
	return sub[:8] + "…"
}

// writeJSONBytes serves an already-encoded JSON body.
func writeJSONBytes(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[policy api] encode: %v", err)
	}
}
