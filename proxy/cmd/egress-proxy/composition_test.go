// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import "testing"

func TestCompositionListsEveryRowAndWhatAModeComposes(t *testing.T) {
	rows, err := parseComposition([]byte(`
plugins:
  - id: webserver
    name: '@deepseek-ai/dsh-host-webserver'
    config: { host: 127.0.0.1, port: 3080 }
  - id: product-analytics
    name: '@deepseek-ai/dsh-product-analytics'
    disabled: true
  - id: tool-pwsh
    name: '@deepseek-ai/dsh-tool-pwsh'
    disabled: !!js process.platform !== 'win32'
  - id: preset-standard
    name: '@deepseek-ai/dsh-agent-preset'
    config:
      id: standard
      plugins:
        - id: persona
          name: '@deepseek-ai/dsh-persona'
          config:
            prefix: You are an agent.
        - id: tool-bash
          name: '@deepseek-ai/dsh-tool-bash'
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d top-level rows: %+v", len(rows), rows)
	}
	if rows[0].ID != "webserver" || rows[0].Name != "@deepseek-ai/dsh-host-webserver" || rows[0].Disabled {
		t.Fatalf("webserver row: %+v", rows[0])
	}
	if !rows[1].Disabled {
		t.Fatalf("a literal disabled: true is off: %+v", rows[1])
	}
	if rows[2].Disabled {
		t.Fatalf("an expression decides at boot and reads as on: %+v", rows[2])
	}
	preset := rows[3]
	if len(preset.Rows) != 2 || preset.Rows[0].ID != "persona" || preset.Rows[1].ID != "tool-bash" {
		t.Fatalf("the preset's plugins: %+v", preset.Rows)
	}
	if len(preset.Rows[0].Rows) != 0 {
		t.Fatalf("a persona's config holds no rows: %+v", preset.Rows[0].Rows)
	}
}
