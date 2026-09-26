// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"log"
	"net/http"
	"strings"
	"sync/atomic"
)

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

// onFirstSubject runs once, when the single-user layout learns who its holder
// is. Nothing on this enclave remembers a holder across restarts, so their
// Drive data is restored at their first request instead of at boot.
var onFirstSubject func(sub string)

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
		if prev == "" && onFirstSubject != nil {
			go onFirstSubject(sub)
		}
	}
}

// currentSubject returns the bound subject, or "" before first sign-in.
func currentSubject() string {
	sub, _ := actingSubject.Load().(string)
	return sub
}

// workerMgr is set when this process supervises one dsh per user (WS5).
// With it, the acting subject is resolved PER REQUEST from what the worker
// holds; without it, the single-user process-wide binding above applies.
var workerMgr *WorkerManager

// subjectOfEgress names the user an egress call (model leg, tool shim,
// loopback storage) acts for. In the per-user layout the caller is a dsh
// worker presenting the bearer this proxy minted for it, and that bearer
// alone names the subject — the model cannot choose another user, it can
// only present the token it was started with. A bearer that matches no
// worker names nobody.
func subjectOfEgress(r *http.Request) string {
	if workerMgr == nil {
		return currentSubject()
	}
	if w := workerMgr.ByToken(workerToken(r)); w != nil {
		return w.Subject
	}
	return ""
}

// workerToken is the bearer a dsh worker presents to this proxy. A client
// speaking OpenAI's wire sends it as `Authorization: Bearer`; one speaking
// Anthropic's Messages wire sends it as `x-api-key`, and dsh 0.1.7-rc.2's
// model adapter (the api-key face of the split llm-deepseek) is the latter.
// Reading Authorization alone attributed every model call to nobody, so no
// spend token went with it and Confidential AI billed the harness app, which
// has no account: "Request quota exhausted" on the first prompt (2026-09-26).
func workerToken(r *http.Request) string {
	if tok := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); tok != "" {
		return tok
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// dropWorkerAPIKey removes an x-api-key that is a worker's own bearer. Like
// its Authorization twin it names the worker to THIS proxy and must never
// leave it; forwarded, it would hand the model's operator a credential that
// acts as that user here.
func dropWorkerAPIKey(h http.Header) {
	if workerMgr == nil {
		return
	}
	if k := strings.TrimSpace(h.Get("X-Api-Key")); k != "" && workerMgr.ByToken(k) != nil {
		h.Del("X-Api-Key")
	}
}

// forwardableAuthorization returns the caller's Authorization header when it
// is a credential to carry upstream, and "" when it is a worker's own bearer:
// that token names the worker to THIS proxy and must never leave it. A tool
// app receiving it would (rightly) refuse it as a malformed token, and the
// attested peer identity plus X-Privasys-On-Behalf-Of is the credential the
// on-platform legs actually use.
func forwardableAuthorization(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" || workerMgr == nil {
		return auth
	}
	tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer"))
	if workerMgr.ByToken(tok) != nil {
		return ""
	}
	return auth
}

// subjectOfShell names the user behind a shell egress connection (the
// forward proxy): the worker runs as its own uid, and the kernel's record of
// who owns the client socket names it. No header is involved, so nothing a
// process runs can claim to be someone else.
func subjectOfShell(remoteAddr string) string {
	if workerMgr == nil {
		return currentSubject()
	}
	uid, err := uidOfLoopbackPeer(remoteAddr)
	if err != nil {
		return ""
	}
	if w := workerMgr.ByUID(uid); w != nil {
		return w.Subject
	}
	return ""
}
