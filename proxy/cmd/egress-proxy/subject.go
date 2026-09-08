// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// rememberedSubjectFile keeps the last signed-in subject across container
// restarts, so the holder's Drive data can be restored BEFORE dsh starts:
// dsh builds its workspace list once at boot and never re-bootstraps, so a
// session restored after that start is invisible until the next restart.
//
// What is stored is one opaque pairwise identifier — not a name, not a
// grant, not content — and it is the one per-user fact this single-user
// deployment keeps on its volume. The clean replacement is a runtime call
// listing the app's approved subjects, which needs no local memory at all;
// until the runtime offers it, this file is the bridge.
const rememberedSubjectFile = "last-subject"

func rememberedSubjectPath() string {
	return filepath.Join(envOr("HARNESS_POLICY_DIR", "/data/policy"), rememberedSubjectFile)
}

// loadRememberedSubject returns the subject noted by the previous run, or "".
func loadRememberedSubject() string {
	raw, err := os.ReadFile(rememberedSubjectPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func rememberSubject(sub string) {
	path := rememberedSubjectPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(sub+"\n"), 0o600)
}

// Acting subject for the SINGLE-USER deployment model: the sealed relay
// asserts the signed-in user's pairwise subject as X-Privasys-Sub on every
// unsealed ingress request (the enclave manager strips any client-supplied
// occurrence first, so the value is relay-authenticated, never caller
// input). The ingress front records it here; tool egress stamps it as
// X-Privasys-On-Behalf-Of so a user-scoped tool app (Drive) can act for
// that user. The binding runs entirely in this measured Go layer — neither
// the model nor dsh's Node code can influence which subject is named.
//
// Single-user by design (one deployment, one user): last-writer-wins, and a
// change of subject is logged loudly because it should never happen until
// the multi-user rails land (per-request subject threading replaces this
// process-level value then).
var actingSubject atomic.Value // string

// recordSubject notes the relay-asserted subject from one ingress request.
func recordSubject(sub string) {
	if sub == "" {
		return
	}
	if prev, _ := actingSubject.Load().(string); prev != sub {
		if prev != "" {
			log.Printf("[ingress] acting subject CHANGED (%.8s… -> %.8s…) — single-user deployment saw a second user", prev, sub)
		} else {
			log.Printf("[ingress] acting subject bound (%.8s…)", sub)
		}
		actingSubject.Store(sub)
		rememberSubject(sub)
	}
}

// currentSubject returns the bound subject, or "" before first sign-in.
func currentSubject() string {
	sub, _ := actingSubject.Load().(string)
	return sub
}
