// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// What this deployment runs: the composition that boots, as the build dumped
// and asserted it (Dockerfile: `dsh --dump-config` over the measured profile,
// kept at /app/composition.yml, so it is part of the image the attestation
// covers). The Attestation panel lists it, beside the evidence that it is
// measured, as "What this deployment runs": every row, on or off, with the
// rows each agent mode composes under it. Not a setting: nothing here can be
// changed from the browser, which is the point.
//
//	GET /privasys/composition   {image, rows:[{id,name,disabled,rows:[…]}], per_user:[…]}

import (
	"log"
	"net/http"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// compositionRow is one Cordis row of the dump: its id, module and whether
// it is off, with the rows composed under it (an agent preset's plugins).
type compositionRow struct {
	ID       string           `json:"id"`
	Name     string           `json:"name,omitempty"`
	Disabled bool             `json:"disabled,omitempty"`
	Rows     []compositionRow `json:"rows,omitempty"`
}

// perUserRows names what this proxy adds to every worker's composition at
// start (workers.go prepare): the same for every user, so listed once.
var perUserRows = []compositionRow{
	{ID: "webserver", Name: "@deepseek-ai/dsh-host-webserver"},
	{ID: "session-persistence-jsonl", Name: "@deepseek-ai/dsh-session-persistence-jsonl"},
	{ID: "webhook-runtime", Name: "@deepseek-ai/dsh-webhook"},
	{ID: "privasys-routines", Name: "privasys-routines (the routines door)"},
	{ID: "privasys-connectors", Name: "privasys-connectors (the connectors switch)"},
	{ID: "privasys-profile", Name: "privasys-profile (your name and instructions)"},
}

// parseComposition walks the dump and returns its rows. A row is any
// mapping with a string `id` and a string `name` (an agent preset's config
// carries an `id` of its own, and is not a row); what a row composes is every
// row found under its `config`. Anything else in the document (options,
// expressions) is passed over: the list says what runs, not how it is
// configured.
func parseComposition(raw []byte) ([]compositionRow, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return rowsIn(&doc), nil
}

func rowsIn(n *yaml.Node) []compositionRow {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.DocumentNode:
		var out []compositionRow
		for _, c := range n.Content {
			out = append(out, rowsIn(c)...)
		}
		return out
	case yaml.SequenceNode:
		var out []compositionRow
		for _, c := range n.Content {
			out = append(out, rowsIn(c)...)
		}
		return out
	case yaml.MappingNode:
		if row, ok := rowOf(n); ok {
			return []compositionRow{row}
		}
		var out []compositionRow
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, rowsIn(n.Content[i+1])...)
		}
		return out
	}
	return nil
}

// rowOf reads one mapping as a row when it has a scalar `id` and `name`.
func rowOf(m *yaml.Node) (compositionRow, bool) {
	var row compositionRow
	var config *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		switch k.Value {
		case "id":
			if v.Kind == yaml.ScalarNode {
				row.ID = v.Value
			}
		case "name":
			if v.Kind == yaml.ScalarNode {
				row.Name = v.Value
			}
		case "disabled":
			// A literal true is off; an expression (!!js) decides at boot and
			// is reported as on, which is what the dump of a web profile means.
			row.Disabled = v.Kind == yaml.ScalarNode && v.Value == "true" && v.Tag != "!!js"
		case "config":
			config = v
		}
	}
	if row.ID == "" || row.Name == "" {
		return compositionRow{}, false
	}
	row.Rows = rowsIn(config)
	return row, true
}

var (
	compositionOnce sync.Once
	compositionRows []compositionRow
)

func loadComposition() []compositionRow {
	compositionOnce.Do(func() {
		path := envOr("HARNESS_COMPOSITION", "/app/composition.yml")
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[composition] %s: %v (the panel will say the list is unavailable)", path, err)
			return
		}
		rows, err := parseComposition(raw)
		if err != nil {
			log.Printf("[composition] %s: %v", path, err)
			return
		}
		compositionRows = rows
		log.Printf("[composition] %d top-level row(s) from %s", len(rows), path)
	})
	return compositionRows
}

func registerCompositionAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /privasys/composition", func(w http.ResponseWriter, r *http.Request) {
		rows := loadComposition()
		if rows == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the composition is not available in this image"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"image":    os.Getenv("PRIVASYS_IMAGE_DIGEST"),
			"rows":     rows,
			"per_user": perUserRows,
		})
	})
}
