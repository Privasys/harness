// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/coder/websocket"
)

// Where each connector stands for the signed-in holder, for the composer's
// Connectors chip: approved (and for which account), not approved, or
// declined. The chip's switch says whether a conversation MAY use a
// connector; this says whether the holder's approval is there for it to use.
// A revoke in the wallet reaches the runtime, whose event stream (events.go)
// wakes the socket below, so the chip changes the moment the holder revokes
// rather than when the agent next trips over it.

// connectorState is one connector's standing for one holder.
type connectorState struct {
	Name string `json:"name"`
	// State is "approved", "not_approved", "declined", "open" for a tool
	// that needs no approval (web search), or "unknown" when the runtime
	// could not be asked.
	State string `json:"state"`
	// Account, Product and Provider are what the connector declared when the
	// holder approved it (the grant's service_result), when it did.
	Account  string `json:"account,omitempty"`
	Product  string `json:"product,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// connectorStates answers for every mounted tool.
func connectorStates(sub string, toolHosts map[string]string, legs []resourceLeg) []connectorState {
	out := make([]connectorState, 0, len(toolHosts))
	for name, host := range toolHosts {
		s := connectorState{Name: name, State: "open"}
		if res := resourceForTool(sub, name, host); res != "" {
			s.State = "unknown"
			for _, l := range legs {
				if l.name != res || !l.broker.Enabled() {
					continue
				}
				st, err := l.broker.Status(sub)
				if err != nil {
					log.Printf("[connectors] status %s: %v", res, err)
					break
				}
				switch {
				case st.Persistent:
					s.State = "approved"
					if st.Granted != nil {
						s.Account = st.Granted.ServiceResult["account"]
						s.Product = st.Granted.ServiceResult["product"]
						s.Provider = st.Granted.ServiceResult["provider"]
					}
				case st.Declined:
					s.State = "declined"
				default:
					s.State = "not_approved"
				}
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// registerConnectorsAPI mounts the answer and its push. Never a held HTTP
// request (the sealed transport answers unary calls in order): the push is a
// sealed WebSocket, written on every event about the holder and every 25
// seconds as a keepalive carrying the current truth.
func registerConnectorsAPI(mux *http.ServeMux, toolHosts map[string]string, legs []resourceLeg, notify *subjectNotifier) {
	mux.HandleFunc("GET /privasys/connectors/status", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		writeJSON(w, http.StatusOK, map[string]any{"signed_in": sub != "", "connectors": connectorStates(sub, toolHosts, legs)})
	})
	mux.HandleFunc("GET /privasys/connectors/events", func(w http.ResponseWriter, r *http.Request) {
		sub := r.Header.Get("X-Privasys-Sub")
		if sub == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "a signed-in session is required"})
			return
		}
		// The relay dials this from inside the enclave with the subject it
		// asserted; there is no browser origin to check here.
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			log.Printf("[connectors] events: accept: %v", err)
			return
		}
		defer c.CloseNow()
		ctx := c.CloseRead(r.Context())
		for {
			body, err := json.Marshal(map[string]any{"signed_in": true, "connectors": connectorStates(sub, toolHosts, legs)})
			if err != nil {
				return
			}
			if err := c.Write(ctx, websocket.MessageText, body); err != nil {
				return
			}
			if notify == nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(25 * time.Second):
				}
				continue
			}
			notify.Wait(ctx, sub, 25*time.Second)
			if ctx.Err() != nil {
				return
			}
		}
	})
}
