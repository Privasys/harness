// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Declared resources: what this deployment asks the holder for.
//
// The harness itself owns no user data. Everything it touches on a person's
// behalf is a RESOURCE the enclave runtime brokers for it: a folder in their
// Drive, a mailbox a connector serves, whatever the next service adds. The
// list is the deployment's declaration, in two places that must agree:
//
//   - the measured manifest (Dockerfile LABEL org.privasys.manifest), which
//     the control plane hands to the runtime so what a holder is TOLD the app
//     wants is attested; and
//   - HARNESS_RESOURCES, the same JSON array in the environment, from which
//     this proxy builds one capability broker per entry.
//
// Adding a resource is therefore a Dockerfile change (one ARG), not code: the
// brokers, the browser endpoints (capability_resources.go) and the access
// server the agent talks to (access.go) all derive from the list. One entry
// is special only in what the proxy DOES with it: the storage folder, named
// by HARNESS_STORAGE_RESOURCE, is where the session mirror, the policy
// documents and the skills live, so its broker is also the mirror's.

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
)

// resourceDecl is one manifest entry, as declared.
type resourceDecl struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Permissions []string `json:"permissions"`
	// Options ride to the manifest label verbatim (app_storage: unattended).
	Options map[string]any `json:"options,omitempty"`
}

// defaultResources is the generic harness's declaration: the holder's folder
// and nothing else. A deployment that mounts a connector appends its
// resource in the Dockerfile.
var defaultResources = []resourceDecl{{
	Kind: "storage.folder", Name: "storage", Label: "Harness",
	Permissions: []string{"read", "write", "delete"},
}}

// declaredResources reads HARNESS_RESOURCES; unset or empty means the default.
// A malformed value is a build error surfaced at boot, not a silent default:
// the manifest label was built from the same text.
func declaredResources() []resourceDecl {
	raw := strings.TrimSpace(os.Getenv("HARNESS_RESOURCES"))
	if raw == "" {
		return defaultResources
	}
	var decls []resourceDecl
	if err := json.Unmarshal([]byte(raw), &decls); err != nil {
		log.Fatalf("[capability] HARNESS_RESOURCES is not a JSON array of resources: %v", err)
	}
	for i, d := range decls {
		if d.Name == "" || d.Kind == "" {
			log.Fatalf("[capability] HARNESS_RESOURCES entry %d needs a name and a kind: %+v", i, d)
		}
	}
	return decls
}

// resourceLegsFor builds one broker per declared resource. The storage
// entry's broker is returned apart because the mirror is built on it; it is
// also in the legs, so the browser endpoints and the access server see every
// resource through one list.
func resourceLegsFor(decls []resourceDecl, storageName string) (storage *capability.Broker, legs []resourceLeg) {
	for _, d := range decls {
		b := capability.NewBroker(d.Name)
		if d.Name == storageName {
			storage = b
		}
		legs = append(legs, resourceLeg{name: d.Name, kind: d.Kind, broker: b})
	}
	if storage == nil {
		// The mirror needs a folder even when a deployment declares none by
		// that name; its broker then answers "not declared" to every ask,
		// which is the honest state.
		storage = capability.NewBroker(storageName)
	}
	return storage, legs
}

// peerAppIDOf is the app id a verified dial found behind a tool's host
// (attested.RATLSTransport.PeerAppID); set by main, nil off the platform.
var peerAppIDOf func(host string) string

// resourceForTool names the declared resource a mounted tool serves for the
// holder, or "" when none can be named.
//
// Nothing in the declaration ties a tool to a resource, and nothing should:
// a tool is a name and a host, a resource is a kind the runtime brokers, and
// which app serves a kind on THIS fleet is the control plane's decision, not
// the image's. The two meet through attestation: the tool's host has been
// proved to be some app (the transport keeps that finding), and the runtime
// reports, per resource, the app that serves it here (`resource_app`). A
// resource whose service is the app answering for the tool is the tool's.
// When no dial has proved the host yet, or the runtime names no match, the
// declared kind's first segment is read as the tool's name (`<tool>.<what>`,
// the same shape an event source has), which is the convention a
// deployment follows when it adds a connector as one tool, one host and one
// resource.
func resourceForTool(sub, tool, host string) string {
	legs := currentAccessLegs()
	if sub == "" || len(legs) == 0 {
		return ""
	}
	if peerAppIDOf != nil && host != "" {
		if appID := peerAppIDOf(host); appID != "" {
			for _, l := range legs {
				st, err := l.broker.Status(sub)
				if err == nil && st != nil && st.ResourceApp != "" && strings.EqualFold(st.ResourceApp, appID) {
					return l.name
				}
			}
		}
	}
	for _, l := range legs {
		if prefix, _, ok := strings.Cut(l.kind, "."); ok && prefix == tool {
			return l.name
		}
	}
	return ""
}

// resourceNames lists the declared names for logs.
func resourceNames(legs []resourceLeg) string {
	names := make([]string, 0, len(legs))
	for _, l := range legs {
		names = append(names, l.name)
	}
	return strings.Join(names, ", ")
}
