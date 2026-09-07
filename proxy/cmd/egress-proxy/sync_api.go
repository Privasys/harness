// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Mirror status and an on-demand flush, on the loopback listener.
//
//	GET  /storage/sync    where the holder's sessions are, and whether they got there
//	POST /storage/sync    mirror now (called at session end rather than waiting a tick)

import (
	"log"
	"net/http"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

func registerSyncAPI(mux *http.ServeMux, syncer *capability.Syncer) {
	mux.HandleFunc("GET /storage/sync", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, syncer.Status())
	})
	mux.HandleFunc("POST /storage/sync", func(w http.ResponseWriter, _ *http.Request) {
		if err := syncer.SyncOnce(); err != nil {
			// Not an error the caller can act on: the mirror retries on its own
			// interval and the local log is already durable. Report it, do not
			// fail the request that triggered it.
			log.Printf("[sync] on-demand mirror: %v", err)
		}
		syncer.SaveState()
		writeJSON(w, http.StatusOK, syncer.Status())
	})
}
