// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The loopback storage leg: how dsh's session store reaches the holder's own
// Drive folder without ever holding a credential.
//
// The split follows D2. Everything that authorises lives in this measured Go
// layer and the runtime behind it — the AppGrant token (signed by the
// runtime's per-app binding key), the attested client identity Drive matches
// against the grant's subject. Node asks for a file by name and receives
// bytes; it cannot widen a scope, reach outside the granted folder, or mint a
// token, because it never sees a key.
//
//	GET    /storage/files              list the approved folder
//	GET    /storage/files/{name}       read one file
//	PUT    /storage/files/{name}       write one file
//	GET    /storage/status             whether storage is available, and why not
//
// Loopback only, on the same listener as the attested tool shims. A request
// arriving here has already crossed no network.

import (
	"io"
	"log"
	"net/http"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// maxSessionBytes bounds one stored object. Session logs are compressed JSONL
// and small; a cap keeps a runaway writer from filling the holder's Drive.
const maxSessionBytes = 32 << 20

func registerStorageAPI(mux *http.ServeMux, broker *capability.Broker,
	client *http.Client, driveHost string) {

	// storeFor resolves the acting holder's capability into a working store,
	// or explains why there is none. The reason is surfaced to Node so the UI
	// can say something true rather than "storage failed".
	storeFor := func() (*capability.DriveStore, string) {
		sub := currentSubject()
		if sub == "" {
			return nil, "no signed-in user is bound to this harness yet"
		}
		if !broker.Enabled() {
			return nil, "no runtime broker: this harness is not running on the platform"
		}
		g := broker.Granted(sub)
		if !g.Usable() {
			return nil, "this user has not connected their Drive; sessions are kept in memory only"
		}
		if driveHost == "" {
			return nil, "no Drive host is configured for this deployment"
		}
		ds, err := capability.NewDriveStore(client, driveHost, broker, g)
		if err != nil {
			return nil, err.Error()
		}
		return ds, ""
	}

	mux.HandleFunc("GET /storage/status", func(w http.ResponseWriter, _ *http.Request) {
		ds, why := storeFor()
		writeJSON(w, http.StatusOK, map[string]any{
			"available": ds != nil,
			"reason":    why,
		})
	})

	mux.HandleFunc("GET /storage/files", func(w http.ResponseWriter, _ *http.Request) {
		ds, why := storeFor()
		if ds == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": why})
			return
		}
		nodes, err := ds.List()
		if err != nil {
			log.Printf("[storage] list failed: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": nodes})
	})

	mux.HandleFunc("GET /storage/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		ds, why := storeFor()
		if ds == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": why})
			return
		}
		name := r.PathValue("name")
		nodes, err := ds.List()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		for _, n := range nodes {
			if n.Name != name {
				continue
			}
			data, err := ds.Get(n.ID)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such file"})
	})

	mux.HandleFunc("PUT /storage/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		ds, why := storeFor()
		if ds == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": why})
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxSessionBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		id, err := ds.Put(r.PathValue("name"), data)
		if err != nil {
			log.Printf("[storage] write %s failed: %v", r.PathValue("name"), err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "bytes": len(data)})
	})

	log.Printf("[storage] loopback leg mounted (drive=%s)", driveHost)
}
