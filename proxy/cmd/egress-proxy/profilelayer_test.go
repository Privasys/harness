// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What dsh's own writers produce (settings, the default-model selection)
// survives untouched in meaning.
func TestUserLayerKeepsConfigEntries(t *testing.T) {
	raw := []byte(`- id: agent-default-model
  config:
    provider: deepseek-official
    model: qwen36-35b-a3b-fp8
- id: permission
  config:
    defaultPreset: workspace-write
`)
	kept, dropped := sanitizeUserLayer(raw)
	if len(dropped) != 0 {
		t.Fatalf("dropped %v from a config-only layer", dropped)
	}
	s := string(kept)
	for _, want := range []string{"agent-default-model", "qwen36-35b-a3b-fp8", "permission", "defaultPreset: workspace-write"} {
		if !strings.Contains(s, want) {
			t.Fatalf("kept layer lost %q:\n%s", want, s)
		}
	}
}

// Everything a settings writer never produces is dropped: new rows, module
// names, file paths, `disabled` flips, groups and evaluated `!!js` values.
func TestUserLayerDropsWhatDshDoesNotWrite(t *testing.T) {
	cases := map[string]string{
		"insert block":        "- insert:\n    - id: x\n      name: '@deepseek-ai/dsh-tool-web'\n",
		"module name":         "- id: tool-web\n  name: '@deepseek-ai/dsh-tool-web'\n",
		"file path name":      "- id: evil\n  name: file:///tmp/evil.js\n",
		"disabled flip":       "- id: ui-cordis\n  disabled: false\n",
		"js expression":       "- id: agent-default-model\n  config:\n    model: !!js process.exit(1)\n",
		"js in nested config": "- id: permission\n  config:\n    nested:\n      deeper: !!js require('fs')\n",
		"custom tag":          "- id: permission\n  config: !foo {a: 1}\n",
		"group":               "- id: g\n  group: true\n  config: {}\n",
		"no id":               "- config:\n    a: 1\n",
		"config not a map":    "- id: permission\n  config: [1, 2]\n",
	}
	for name, raw := range cases {
		kept, dropped := sanitizeUserLayer([]byte(raw))
		if kept != nil || len(dropped) != 1 {
			t.Errorf("%s: kept %q, dropped %v", name, kept, dropped)
		}
	}
}

// One bad entry costs only itself: the settings around it survive.
func TestUserLayerKeepsGoodEntriesBesideABadOne(t *testing.T) {
	raw := []byte(`- id: agent-default-model
  config:
    model: qwen36-35b-a3b-fp8
- id: evil
  name: file:///tmp/evil.js
`)
	kept, dropped := sanitizeUserLayer(raw)
	if len(dropped) != 1 || dropped[0] != "evil" {
		t.Fatalf("dropped = %v, want [evil]", dropped)
	}
	if !strings.Contains(string(kept), "qwen36-35b-a3b-fp8") || strings.Contains(string(kept), "evil") {
		t.Fatalf("kept the wrong part:\n%s", kept)
	}
}

func TestUserLayerShapes(t *testing.T) {
	if kept, dropped := sanitizeUserLayer(nil); kept != nil || dropped != nil {
		t.Fatal("an empty layer must stay empty with nothing reported")
	}
	if kept, dropped := sanitizeUserLayer([]byte("id: x\nname: y\n")); kept != nil || len(dropped) != 1 {
		t.Fatal("a layer that is not a list must be dropped whole")
	}
	if kept, dropped := sanitizeUserLayer([]byte("- id: [unclosed\n")); kept != nil || len(dropped) != 1 {
		t.Fatal("an unparseable layer must be dropped whole")
	}
}

// refreshProfiles restores each profile's user layer through the filter, so a
// planted row does not come back at the next worker start.
func TestRefreshProfilesFiltersTheKeptLayer(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "web", "package.json"), []byte(`{"name":"p"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	layer := "- id: agent-default-model\n  config:\n    model: qwen36-35b-a3b-fp8\n- insert:\n    - id: evil\n      name: file:///tmp/evil.js\n"
	if err := os.WriteFile(filepath.Join(dst, "web", "cordis.patch.yml"), []byte(layer), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := refreshProfiles(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "web", "cordis.patch.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "evil") || !strings.Contains(string(got), "qwen36-35b-a3b-fp8") {
		t.Fatalf("restored layer:\n%s", got)
	}
}
