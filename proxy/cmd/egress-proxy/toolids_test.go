// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A pinned call's tool-call ids are a function of prompt digest, seed and
// ordinal: identical across a replay, distinct per call, and later deltas
// of the same call keep the same rewritten id. An unpinned call is served
// as vLLM sent it.
func TestRewriteToolCallIDs_DeterministicForPinnedCalls(t *testing.T) {
	first := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"chatcmpl-tool-abc","function":{"name":"f","arguments":""}}]}}]}` + "\n"
	later := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"chatcmpl-tool-abc","function":{"arguments":"{}"}}]}}]}` + "\n"
	second := `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"chatcmpl-tool-xyz","function":{"name":"g","arguments":""}}]}}]}` + "\n"
	idOf := func(line []byte) string {
		var f struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct{ ID string `json:"id"` } `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(string(line)), "data: ")), &f); err != nil {
			t.Fatal(err)
		}
		return f.Choices[0].Delta.ToolCalls[0].ID
	}
	run := func() (string, string, string) {
		b := &reproScanBody{call: &modelCall{PromptDigest: "abc", Pins: &samplingPins{Seed: i64(7)}}}
		return idOf(b.scan([]byte(first))), idOf(b.scan([]byte(later))), idOf(b.scan([]byte(second)))
	}
	a1, a2, a3 := run()
	b1, _, b3 := run()
	if !strings.HasPrefix(a1, "call_") || len(a1) != 5+24 {
		t.Fatalf("rewritten id has the wrong shape: %q", a1)
	}
	if a1 != b1 || a3 != b3 {
		t.Fatalf("ids differ across an identical replay: %q/%q vs %q/%q", a1, a3, b1, b3)
	}
	if a1 != a2 {
		t.Fatalf("a later delta of the same call changed id: %q vs %q", a1, a2)
	}
	if a1 == a3 {
		t.Fatal("two calls of one step share an id")
	}
	other := &reproScanBody{call: &modelCall{PromptDigest: "def", Pins: &samplingPins{Seed: i64(7)}}}
	if idOf(other.scan([]byte(first))) == a1 {
		t.Fatal("a different prompt must yield a different id")
	}
	unpinned := &reproScanBody{call: &modelCall{PromptDigest: "abc"}}
	if got := idOf(unpinned.scan([]byte(first))); got != "chatcmpl-tool-abc" {
		t.Fatalf("unpinned call rewritten: %q", got)
	}
}
