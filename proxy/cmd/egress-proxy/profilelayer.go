// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// The user layer of a worker's dsh profile, held to what dsh itself writes.
//
// dsh composes a profile from its bundles, then the profile's own patch file
// ($DSH_HOME/profiles/<name>/cordis.patch.yml), then a home-level patch
// ($DSH_HOME/cordis.patch.yml), then the deployment's --patch. Those two
// middle files live in the worker's dsh home, the holder's folder, so they
// outlive a restart: refreshProfiles deliberately keeps the profile patch,
// because dsh's settings write there.
//
// Those files can do far more than settings ever ask of them. A row there may
// name any installed package (including ones this deployment's allow-list
// excludes), any JS file on disk, or carry a `!!js` value that dsh evaluates in
// its own process. The deployment's --patch overrides what it names but cannot
// remove a row added beneath it. So at every worker start the proxy keeps only
// what dsh's own writers produce, and the home-level file, which nothing here
// writes, is removed:
//
//   - each entry names a row by `id` and carries at most a `config` mapping;
//     `name`, `insert`, `disabled`, groups and every other key drop the entry
//     (settings and the default-model selection write `config` only; the
//     plugin manager, which writes `disabled`, is not part of this build);
//   - no `!!js` (or any other custom tag) anywhere in a kept entry;
//   - a file that is not a YAML sequence of mappings is dropped whole.
//
// Dropped entries are logged by id, never by content.

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// sanitizeUserLayer returns the part of a profile's user layer to keep, and
// the ids (or positions) of the entries it dropped.
func sanitizeUserLayer(raw []byte) (kept []byte, dropped []string) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, []string{"(unparseable layer)"}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil, []string{"(layer is not a list of entries)"}
	}
	seq := doc.Content[0]
	out := &yaml.Node{Kind: yaml.SequenceNode}
	for i, entry := range seq.Content {
		id, ok := configOnlyEntry(entry)
		if !ok {
			if id == "" {
				id = fmt.Sprintf("(entry %d)", i)
			}
			dropped = append(dropped, id)
			continue
		}
		out.Content = append(out.Content, entry)
	}
	if len(out.Content) == 0 {
		return nil, dropped
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return nil, append(dropped, "(re-encode failed)")
	}
	_ = enc.Close()
	return buf.Bytes(), dropped
}

// configOnlyEntry reports whether entry is `{id: <string>, config?: <mapping>}`
// with no custom tag anywhere, and returns its id when it has one.
func configOnlyEntry(entry *yaml.Node) (string, bool) {
	if entry.Kind != yaml.MappingNode || hasCustomTag(entry) {
		return idOf(entry), false
	}
	id, sawID := "", false
	for i := 0; i+1 < len(entry.Content); i += 2 {
		key, val := entry.Content[i], entry.Content[i+1]
		switch key.Value {
		case "id":
			if val.Kind != yaml.ScalarNode || strings.TrimSpace(val.Value) == "" {
				return "", false
			}
			id, sawID = val.Value, true
		case "config":
			if val.Kind != yaml.MappingNode {
				return idOf(entry), false
			}
		default:
			return idOf(entry), false
		}
	}
	return id, sawID
}

// hasCustomTag reports a non-core tag (`!!js`, `!foo`) anywhere under n. Plain
// scalars resolve to core tags (!!str, !!int, ...) and are fine.
func hasCustomTag(n *yaml.Node) bool {
	switch n.Tag {
	case "", "!!str", "!!int", "!!float", "!!bool", "!!null", "!!map", "!!seq", "!!timestamp", "!!binary", "!!merge":
	default:
		return true
	}
	for _, c := range n.Content {
		if hasCustomTag(c) {
			return true
		}
	}
	if n.Alias != nil {
		return hasCustomTag(n.Alias)
	}
	return false
}

func idOf(entry *yaml.Node) string {
	if entry.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(entry.Content); i += 2 {
		if entry.Content[i].Value == "id" && entry.Content[i+1].Kind == yaml.ScalarNode {
			return entry.Content[i+1].Value
		}
	}
	return ""
}
