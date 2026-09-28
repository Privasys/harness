// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import "sort"

// attestedTool is one mounted tool as the attestation summary shows it.
type attestedTool struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Host  string `json:"host"`
	// AppID is the app the host proved to be on the last connection that
	// passed the dependency gate; empty until one has.
	AppID string `json:"app_id,omitempty"`
}

// attestedTools lists every mounted tool with the app its host proved to be,
// labelled as the composer labels it (HARNESS_CONNECTOR_LABELS).
func attestedTools(toolHosts map[string]string, appOf func(host string) string) []attestedTool {
	labels := connectorLabels()
	out := make([]attestedTool, 0, len(toolHosts))
	for name, host := range toolHosts {
		t := attestedTool{Name: name, Label: connectorLabel(name), Host: host}
		if l := labels[name][0]; l != "" {
			t.Label = l
		}
		if appOf != nil {
			t.AppID = appOf(host)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
