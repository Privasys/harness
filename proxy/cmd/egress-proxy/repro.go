// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// Reproducibility capture for the /model leg.
//
// Confidential AI emits its reproducibility block only to callers that opt in
// (X-Privasys-Reproducibility), because stock OpenAI clients reject unknown
// trailing SSE frames. dsh's SSE translator skips frames without `choices`,
// so the proxy opts in ON BEHALF of the harness and the frame passes through
// to the plugins untouched — a dsh-side renderer can surface it later, while
// the proxy already records the audit line: every model call's seed, model,
// and dependency fold, captured on the attested leg itself (D2: the trust
// artifact is handled by the measured Go side, never left to Node to
// request or drop).

// injectReproOptIn opts the upstream request into the reproducibility
// extension unless the caller already chose.
func injectReproOptIn(req *http.Request) {
	if req.Header.Get("X-Privasys-Reproducibility") == "" {
		req.Header.Set("X-Privasys-Reproducibility", "1")
	}
}

// reproFields is the subset of the reproducibility block worth an audit line.
type reproFields struct {
	RequestID      string `json:"request_id"`
	Model          string `json:"model"`
	Seed           *int64 `json:"seed"`
	VLLMVersion    string `json:"vllm_version"`
	CachedTokens   *int64 `json:"cached_tokens"`
	DependencyFold string `json:"dependency_fold"`
	KVCacheMode    string `json:"kv_cache_mode"`
	// DynamicContext is the clock Confidential AI stamped into the prompt:
	// the one prompt element the proxy's digest cannot see, and the one a
	// replay must hand back through X-Privasys-Dynamic-Context. A clock a
	// few seconds apart changes the whole reply (measured 2026-09-10), so
	// its digest is logged and, under a replay step, checked.
	DynamicContext string `json:"dynamic_context"`
}

// shortDigest is the first 12 hex digits of SHA-256(s), "-" for "".
func shortDigest(s string) string {
	if s == "" {
		return "-"
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// logRepro writes the audit line for one reproducibility block and returns
// the parsed fields.
func logRepro(where string, raw json.RawMessage, call *modelCall) {
	var f reproFields
	if err := json.Unmarshal(raw, &f); err != nil {
		return
	}
	seed := int64(-1)
	if f.Seed != nil {
		seed = *f.Seed
	}
	cached := int64(-1)
	if f.CachedTokens != nil {
		cached = *f.CachedTokens
	}
	log.Printf("[egress-proxy repro] %s request_id=%s model=%s seed=%d vllm=%s cached_tokens=%d kv=%s dynctx=%s",
		where, f.RequestID, f.Model, seed, f.VLLMVersion, cached, f.KVCacheMode, shortDigest(f.DynamicContext))
	if call != nil && call.ExpectedDynamicContext != "" {
		match := f.DynamicContext == call.ExpectedDynamicContext
		call.DynamicContextMatch = &match
		if !match {
			log.Printf("[egress-proxy repro] request %s replayed with dynamic context %s but the model stamped %s",
				call.RequestID, shortDigest(call.ExpectedDynamicContext), shortDigest(f.DynamicContext))
		}
	}
}

// reproScanBody wraps an SSE response body: it streams every byte through
// while watching for the trailing `data: {"reproducibility":...}` frame. That
// one frame is logged and, when the request carried a call record
// (sampling.go), rewritten to carry the proxy's `harness` annotation — the
// prompt digest, the pins applied, the replay verdict — so the block dsh
// folds into its session log is the whole story of the call, not only what
// Confidential AI could see. Every other byte passes unchanged.
// Line-oriented (SSE frames are lines); a line longer than the scanner
// buffer passes through unscanned rather than failing the stream.
type reproScanBody struct {
	rc     io.ReadCloser
	br     *bufio.Reader
	buf    []byte // current line remainder being served to the caller
	logged bool
	call   *modelCall
	// lossyFound bounds the dsh-v2 lossless diagnostic (see lossyKeys) to a
	// few findings per response so a long stream cannot flood the log.
	lossyFound int
	// toolFound bounds the tool-call shape probe (structure only, no content).
	toolFound int
	// toolIDs maps the ids vLLM gave this stream's tool calls to the
	// deterministic ids served to dsh (toolids.go); toolSeq numbers them.
	toolIDs map[string]string
	toolSeq int
}

func newReproScanBody(rc io.ReadCloser, call *modelCall) *reproScanBody {
	return &reproScanBody{rc: rc, br: bufio.NewReaderSize(rc, 64<<10), call: call}
}

func (b *reproScanBody) Read(p []byte) (int, error) {
	if len(b.buf) == 0 {
		line, err := b.br.ReadBytes('\n')
		if len(line) > 0 {
			b.buf = b.scan(line)
		}
		if len(b.buf) == 0 {
			return 0, err
		}
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

// scan inspects one SSE line and returns the line to serve: the same bytes,
// or the reproducibility frame with the harness annotation folded in.
func (b *reproScanBody) scan(line []byte) []byte {
	if b.logged {
		return line
	}
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, "data:") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	// dsh session-format-v2 diagnostic: report chunks carrying a value the
	// client refuses to embed (negative zero / non-finite). Runs before the
	// reproducibility filter so it sees EVERY chunk, not just the trailer.
	if b.lossyFound < 3 && payload != "[DONE]" {
		for _, k := range lossyKeys([]byte(payload)) {
			log.Printf("[egress-proxy] model chunk carries a value dsh v2 refuses: %s", k)
			b.lossyFound++
			if b.lossyFound >= 3 {
				break
			}
		}
	}
	// Tool-call delta SHAPE probe. dsh 0.1.3 diverts any tool-call delta whose
	// id or name is empty into a raw chunk record, which its session-format-v2
	// validation then refuses — so what matters is whether OUR stream carries a
	// non-empty id on a call's first delta. Logs structure only (index, which
	// fields are present, id length): never names, arguments or any content.
	if b.toolFound < 6 && strings.Contains(payload, `"tool_calls"`) {
		var frame struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    *int    `json:"index"`
						ID       *string `json:"id"`
						Function *struct {
							Name      *string `json:"name"`
							Arguments *string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &frame) == nil {
			for _, ch := range frame.Choices {
				for _, tc := range ch.Delta.ToolCalls {
					idLen := -1
					if tc.ID != nil {
						idLen = len(*tc.ID)
					}
					hasName, hasArgs := false, false
					if tc.Function != nil {
						hasName = tc.Function.Name != nil
						hasArgs = tc.Function.Arguments != nil
					}
					idx := -1
					if tc.Index != nil {
						idx = *tc.Index
					}
					log.Printf("[egress-proxy] tool_call delta: index=%d id_len=%d has_name=%v has_arguments=%v",
						idx, idLen, hasName, hasArgs)
					b.toolFound++
				}
			}
		}
	}
	if b.call != nil && !b.call.Pins.empty() && strings.Contains(payload, `"tool_calls"`) {
		if rewritten, ok := b.rewriteToolCallIDs(payload); ok {
			return rewritten
		}
	}
	if !strings.Contains(payload, `"reproducibility"`) {
		return line
	}
	var frame struct {
		Reproducibility json.RawMessage `json:"reproducibility"`
	}
	if err := json.Unmarshal([]byte(payload), &frame); err != nil || frame.Reproducibility == nil {
		return line
	}
	logRepro("stream", frame.Reproducibility, b.call)
	b.logged = true
	if b.call == nil {
		return line
	}
	return annotateReproFrame(line, payload, b.call)
}

// annotateReproFrame merges the call's `harness` annotation into the
// reproducibility object of one SSE data line. A frame that does not decode
// as an object is served unchanged: a lost annotation is a lesser fault than
// a broken stream.
func annotateReproFrame(line []byte, payload string, call *modelCall) []byte {
	var frame map[string]any
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return line
	}
	repro, ok := frame["reproducibility"].(map[string]any)
	if !ok {
		return line
	}
	repro["harness"] = call.annotation()
	out, err := json.Marshal(frame)
	if err != nil {
		return line
	}
	return append(append([]byte("data: "), out...), '\n')
}

// lossyKeys reports the JSON key paths in one SSE payload whose numeric value
// dsh's session format v2 refuses. From dsh 0.1.3 every raw model chunk is
// embedded in the session log and validated by snapshotJsonValue, which
// rejects NaN, ±Infinity and NEGATIVE ZERO; a rejected chunk breaks the
// client's event feed on every reconnect ("Assistant stream raw chunk must be
// a lossless JSON object"). JSON.parse cannot yield NaN/Infinity, so -0 is the
// realistic offender — and it is invisible in any normal log, hence this scan.
//
// Diagnostic only: it reports KEY PATHS, never values or user content, and the
// caller logs a bounded number of findings. Remove once the source is fixed.
func lossyKeys(payload []byte) []string {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	var out []string
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch t := node.(type) {
		case map[string]any:
			for k, child := range t {
				p := k
				if path != "" {
					p = path + "." + k
				}
				walk(child, p)
			}
		case []any:
			for i, child := range t {
				walk(child, fmt.Sprintf("%s[%d]", path, i))
			}
		case json.Number:
			s := t.String()
			// Negative zero in any spelling (-0, -0.0, -0e5): the sign is
			// what matters, and every digit before the exponent is zero.
			if strings.HasPrefix(s, "-") {
				mant := strings.TrimPrefix(s, "-")
				if e := strings.IndexAny(mant, "eE"); e >= 0 {
					mant = mant[:e]
				}
				if strings.Trim(mant, "0.") == "" {
					out = append(out, path+" = "+s)
				}
			}
			if strings.ContainsAny(s, "nN") || strings.Contains(s, "Inf") {
				out = append(out, path+" = "+s)
			}
		}
	}
	walk(v, "")
	return out
}

func (b *reproScanBody) Close() error { return b.rc.Close() }
