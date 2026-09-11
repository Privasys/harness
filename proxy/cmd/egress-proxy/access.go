// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The harness's own MCP server: what the agent may use on the user's behalf,
// and how it asks for it.
//
//	/tool/access/mcp   list_access, request_access
//
// Access to a user's resources (a folder in their Drive, their mailbox, the
// next thing a connector serves) is approved by the PERSON, in their wallet,
// and brokered by the runtime; none of that changes here. What this adds is
// the trigger, in the place a personal agent talks to someone: the
// conversation. A tool that finds access missing says so, the agent asks
// the user, and on a yes this server starts the wallet approval.
//
// Deliberately generic. The server knows the resources the measured manifest
// declares and nothing about what any of them is: no screen per product, no
// product name, no service-specific setup. A service that needs something
// before it can be approved (a mailbox linked on its own page, say) says so
// in its own tool errors, and the agent passes that on.
//
// The acting user is the one the calling worker's bearer names, exactly as
// for every other tool leg. The model cannot ask on someone else's behalf,
// and it cannot approve anything: a request only puts a question to the
// person's device.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
)

const accessServerName = "access"

var (
	accessMu   sync.RWMutex
	accessLegs []resourceLeg
)

// setAccessLegs installs the declared resources this server reports on. Legs
// without a name or a broker are skipped.
func setAccessLegs(legs ...resourceLeg) {
	kept := make([]resourceLeg, 0, len(legs))
	for _, l := range legs {
		if l.name != "" && l.broker != nil {
			kept = append(kept, l)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].name < kept[j].name })
	accessMu.Lock()
	accessLegs = kept
	accessMu.Unlock()
}

func currentAccessLegs() []resourceLeg {
	accessMu.RLock()
	defer accessMu.RUnlock()
	return accessLegs
}

func accessTools(legs []resourceLeg) []map[string]any {
	names := make([]string, 0, len(legs))
	for _, l := range legs {
		names = append(names, l.name)
	}
	resource := map[string]any{
		"type":        "string",
		"description": "The resource name, as list_access reports it.",
	}
	if len(names) > 0 {
		resource["enum"] = names
	}
	return []map[string]any{
		{
			"name": "list_access",
			"description": "List the user's own resources this assistant is set up to use on their behalf " +
				"(for example a folder in their Drive, or their mailbox), and whether the user has approved each one. " +
				"Call it when a tool reports that access is missing, not approved or expired.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "request_access",
			"description": "Ask the user to approve this assistant's access to one of their resources. " +
				"The request goes to the user's wallet on their device, which verifies this assistant and shows them exactly what is asked; " +
				"nothing is granted unless they approve it there. Ask the user in the conversation first and call this only once they agree. " +
				"If they declined before, the request is not sent again unless ask_again is true, which you may set only when the user has just told you they changed their mind. " +
				"If a tool said the user must first set something up with the service itself (for example link an account on the service's own page), pass that on: " +
				"never ask the user to type a password or other credential into this conversation.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"resource":  resource,
					"ask_again": map[string]any{"type": "boolean", "description": "Reopen a request the user declined earlier. Only when they have just said they want to be asked again."},
				},
				"required": []string{"resource"},
			},
		},
	}
}

// accessShim handles one JSON-RPC message for the access server. Same
// stateless Streamable HTTP shape as mcpShim.
func accessShim(w http.ResponseWriter, r *http.Request) {
	sub := subjectOfEgress(r)
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		rpcError(w, nil, -32700, "parse error")
		return
	}
	legs := currentAccessLegs()

	switch req.Method {
	case "initialize":
		rpcResult(w, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name":    "privasys-egress-proxy/" + accessServerName,
				"version": "0.1.0",
			},
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		rpcResult(w, req.ID, map[string]any{})
	case "tools/list":
		rpcResult(w, req.ID, map[string]any{"tools": accessTools(legs)})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			rpcError(w, req.ID, -32602, "invalid params")
			return
		}
		out, isErr := callAccessTool(p.Name, p.Arguments, sub, legs)
		text, _ := json.Marshal(out)
		rpcResult(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(text)}},
			"isError": isErr,
		})
	default:
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		rpcError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

// callAccessTool runs one access tool for sub and returns the result the
// model reads, and whether it is an error.
func callAccessTool(name string, args json.RawMessage, sub string, legs []resourceLeg) (map[string]any, bool) {
	if sub == "" {
		return map[string]any{"error": "no signed-in user is bound to this session, so there is nobody to ask"}, true
	}
	switch name {
	case "list_access":
		return listAccess(sub, legs), false
	case "request_access":
		var a struct {
			Resource string `json:"resource"`
			AskAgain bool   `json:"ask_again"`
		}
		if len(args) > 0 {
			if err := json.Unmarshal(args, &a); err != nil {
				return map[string]any{"error": "invalid arguments: " + err.Error()}, true
			}
		}
		return requestAccess(sub, strings.TrimSpace(a.Resource), a.AskAgain, legs)
	}
	return map[string]any{"error": fmt.Sprintf("unknown tool %q", name)}, true
}

func listAccess(sub string, legs []resourceLeg) map[string]any {
	items := make([]map[string]any, 0, len(legs))
	for _, l := range legs {
		item := map[string]any{"resource": l.name}
		if !l.broker.Enabled() {
			item["state"] = "unavailable"
			item["note"] = "this assistant is not running on the platform, so access cannot be approved here"
			items = append(items, item)
			continue
		}
		st, err := l.broker.Status(sub)
		if err != nil {
			// Reported as unknown, never as "not approved": a broker hiccup
			// must not send the agent off to ask for something already given.
			log.Printf("[access] status %s: %v", l.name, err)
			item["state"] = "unknown"
			items = append(items, item)
			continue
		}
		item["kind"] = st.Kind
		if st.Label != "" {
			item["label"] = st.Label
		}
		if len(st.Permissions) > 0 {
			item["permissions"] = st.Permissions
		}
		switch {
		case st.Persistent && st.Stale:
			item["state"] = "approved_needs_update"
			item["note"] = "approved earlier for fewer or other permissions than this assistant now uses; it keeps working, and the user can approve the update"
			item["granted_permissions"] = st.GrantedPermissions
		case st.Persistent:
			item["state"] = "approved"
		case st.Declined:
			item["state"] = "declined"
		default:
			item["state"] = "not_approved"
		}
		if st.Granted != nil && len(st.Granted.ServiceResult) > 0 {
			// Opaque, from the service that granted it (a connector names the
			// account it approved here). Passed on, never interpreted.
			item["details"] = st.Granted.ServiceResult
		}
		items = append(items, item)
	}
	return map[string]any{"resources": items}
}

func requestAccess(sub, name string, askAgain bool, legs []resourceLeg) (map[string]any, bool) {
	var leg *resourceLeg
	names := make([]string, 0, len(legs))
	for i := range legs {
		names = append(names, legs[i].name)
		if legs[i].name == name {
			leg = &legs[i]
		}
	}
	if leg == nil {
		return map[string]any{"error": fmt.Sprintf("no resource %q; this assistant declares: %s", name, strings.Join(names, ", "))}, true
	}
	if !leg.broker.Enabled() {
		return map[string]any{"error": "this assistant is not running on the platform, so there is nothing to approve"}, true
	}
	out, err := leg.broker.Request(sub, askAgain)
	if err != nil {
		log.Printf("[access] request %s: %v", name, err)
		return map[string]any{"error": err.Error()}, true
	}
	status, _ := out["status"].(string)
	res := map[string]any{"resource": name, "status": status}
	nonce, _ := out["nonce"].(string)
	host, _ := out["app_host"].(string)
	switch {
	case nonce != "":
		res["status"] = "sent"
		res["message"] = "The request is on its way to the user's wallet. Tell them to approve it on their device. " +
			"If no notification arrives, they can open the wallet and enter this host and code by hand."
		res["app_host"] = host
		res["code"] = nonce
	case strings.Contains(status, "declined"):
		res["message"] = "The user declined this earlier, so they were not asked again. " +
			"Only if they tell you now that they want to be asked again, call request_access with ask_again true."
	case strings.Contains(status, "granted") || strings.Contains(status, "approved"):
		res["message"] = "Already approved; nothing to ask."
	default:
		res["runtime"] = out
	}
	return res, false
}
