// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// rewriteToolCallIDs gives the tool calls of a PINNED call deterministic
// ids. vLLM's tool parser draws each id at random; dsh echoes it back in
// the tool-result messages of the next step, so from step 2 on an
// original and its replay carried different prompt bytes ("prompt
// differs") even when the model's words were identical (2026-09-11), and
// nothing pinned the model to ignore them. An id is now
// call_<sha256(prompt digest || seed || ordinal)>: the same across a
// replay (same prompt, same seed), distinct across steps (the prompt
// moves) and across the calls of one step (the ordinal). Later deltas of
// a call repeat its id and are mapped the same way. Returns ok=false when
// the chunk needs no change or cannot be decoded, so it is served as is.
func (b *reproScanBody) rewriteToolCallIDs(payload string) ([]byte, bool) {
	var frame map[string]any
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return nil, false
	}
	choices, _ := frame["choices"].([]any)
	changed := false
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		calls, _ := delta["tool_calls"].([]any)
		for _, tc := range calls {
			call, _ := tc.(map[string]any)
			id, _ := call["id"].(string)
			if id == "" {
				continue
			}
			if b.toolIDs == nil {
				b.toolIDs = map[string]string{}
			}
			newID, seen := b.toolIDs[id]
			if !seen {
				seed := int64(0)
				if b.call.Pins != nil && b.call.Pins.Seed != nil {
					seed = *b.call.Pins.Seed
				}
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", b.call.PromptDigest, seed, b.toolSeq)))
				newID = "call_" + hex.EncodeToString(sum[:])[:24]
				b.toolIDs[id] = newID
				b.toolSeq++
			}
			call["id"] = newID
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(frame)
	if err != nil {
		return nil, false
	}
	return append(append([]byte("data: "), out...), '\n'), true
}
